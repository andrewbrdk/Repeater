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
    {{range .Tasks}}
	<div>
	<details open>
	<summary><strong>{{.Title}}</strong> {{.Cron}}</summary>
	<table>
        <tr>
            <th> Start </th>
			<th> {{.Title}} </th>
			{{range .Commands}}
            	<th> {{.Name}} </th>
			{{end}}
		</tr>
		{{range .History}}
			<tr>
                <td>{{.StartTime.Format "2006-01-02 15:04:05"}}</td>
				<td>{{.Status.WebTableString}}</td>
				{{range .Details}}
					<td>{{.Status.WebTableString}} </td>
				{{end}}
			</tr>
		{{end}}
    </table>
	</details>
	</div>
    {{end}}
</body>
</html>
`

type Status int

const (
	runSuccess Status = iota
	runFailure
)

func (s Status) String() string {
	switch s {
	case runSuccess:
		return "success"
	case runFailure:
		return "failure"
	default:
		return "unknown"
	}
}

func (s Status) WebTableString() string {
	switch s {
	case runSuccess:
		return "s"
	case runFailure:
		return "f"
	default:
		return "?"
	}
}

type Command struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type CommandRun struct {
	Name      string
	Cmd       string
	StartTime time.Time
	EndTime   time.Time
	Status    Status
}

type Run struct {
	StartTime time.Time
	EndTime   time.Time
	Status    Status
	Details   []*CommandRun
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
			slog.Warn("Task with title '%s' already exists, skipping processing.\n", task.Title)
			return
		}
	}
	tasks.Tasks = append(tasks.Tasks, task)
	slog.Info("Added task", task.Title, "from file", path)
}

func runTasks(tasks *AllTasks, c *cron.Cron) {
	for _, t := range tasks.Tasks {
		t := t // Capture d variable
		t.cronJobFunc = func() { runTaskCommands(t) }
		t.cronID, _ = c.AddFunc(t.Cron, t.cronJobFunc)
	}
}

func runTaskCommands(task *Task) {
	slog.Info("Running task", task.Title)

	run := &Run{StartTime: time.Now()}
	defer func() {
		run.EndTime = time.Now()
		task.History = append(task.History, run)
	}()

	for _, c := range task.Commands {
		cmdStartTime := time.Now()
		output, err := executeCommand(c.Cmd)
		cmdEndTime := time.Now()
		cmdStatus := runSuccess
		if err != nil {
			slog.Error("Error executing '%s'-'%s': %v\n", task.Title, c.Name, err)
			cmdStatus = runFailure
			run.Status = runFailure
			return
		}
		slog.Info("Task", task.Title, "command", c.Name, "output", output)

		run.Details = append(run.Details, &CommandRun{
			Name:      c.Name,
			Cmd:       c.Cmd,
			StartTime: cmdStartTime,
			EndTime:   cmdEndTime,
			Status:    cmdStatus,
		})
	}
	run.Status = runSuccess
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
