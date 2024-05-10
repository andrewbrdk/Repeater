package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gookit/slog"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const tasksDir = "./"

const webTasksList = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Tasks</title>
</head>
<body>
    <h1>Tasks</h1>
    <ul>
        {{range .Tasks}}
        <li>
            <strong>{{.Title}}</strong> {{.Cron}}
            {{if .History}}
            <ul>
                {{range $i, $h := .History}}
                <li>
                    Run {{$i}}:
                    <ul>
                        <li>Start Time: {{$h.StartTime.Format "2006-01-02 15:04:05"}}</li>
                        <li>End Time: {{$h.EndTime.Format "2006-01-02 15:04:05"}}</li>
                        <li>Status: {{$h.Status}}</li>
                    </ul>
                </li>
                {{end}}
            </ul>
            {{else}}
                <li>No execution history</li>
            </ul>
            {{end}}
        </li>
        {{end}}
    </ul>
</body>
</html>
`

type Command struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type Run struct {
	StartTime time.Time
	EndTime   time.Time
	Status    string
}

type Task struct {
	Title       string     `json:"title"`
	Cron        string     `json:"cron"`
	Commands    []*Command `json:"commands"`
	cronID      cron.EntryID
	cronJobFunc cron.FuncJob
	History     []*Run
}

type AllTasks struct {
	Tasks []*Task
}

func main() {
	var tasks AllTasks
	c := cron.New(cron.WithSeconds())
	c.Start()
	scanTasks(&tasks)
	runTasks(&tasks, c)
	webServer(&tasks)
}

func webServer(tasks *AllTasks) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		listTasks(w, tasks)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func listTasks(w http.ResponseWriter, tasks *AllTasks) {
	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webTasksList))

	err := tmpl.Execute(w, tasks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func scanTasks(tasks *AllTasks) {
	dir := tasksDir
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Error("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".tasks" {
			task, err := processJSONFile(path)
			if err != nil {
				return err
			}
			addTask(path, task, tasks)
		}
		return nil
	})
	if err != nil {
		slog.Fatal(err)
	}
}

func processJSONFile(filePath string) (*Task, error) {
	var task Task
	jsonFile, err := os.ReadFile(filePath)
	if err != nil {
		slog.Error("Error reading JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	err = json.Unmarshal(jsonFile, &task)
	if err != nil {
		slog.Error("Error parsing JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	return &task, nil
}

func addTask(path string, task *Task, tasks *AllTasks) {
	for _, existing := range tasks.Tasks {
		if existing.Title == task.Title {
			slog.Info("Task with title '%s' already exists, skipping processing.\n", task.Title)
			return
		}
	}
	tasks.Tasks = append(tasks.Tasks, task)
	slog.Info("Added task '%s' from file %s.\n", task.Title, path)
}

func runTasks(tasks *AllTasks, c *cron.Cron) {
	for _, t := range tasks.Tasks {
		t := t // Capture d variable
		t.cronJobFunc = func() { runTaskCommands(t) }
		t.cronID, _ = c.AddFunc(t.Cron, t.cronJobFunc)
	}
}

func runTaskCommands(task *Task) {
	slog.Info("Running task '%s'\n", task.Title)

	run := &Run{StartTime: time.Now()}
	defer func() {
		run.EndTime = time.Now()
		task.History = append(task.History, run)
	}()

	for _, c := range task.Commands {
		output, err := executeCommand(c.Cmd)
		if err != nil {
			slog.Error("Error executing '%s'-'%s': %v\n", task.Title, c.Name, err)
			run.Status = "failure"
			return
		}
		slog.Info("Task", task.Title, "command", c.Name, "output", output)
	}
	run.Status = "success"
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
