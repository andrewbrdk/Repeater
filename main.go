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
			{{range .Tasks}}
            	<th> {{.Name}} </th>
			{{end}}
		</tr>
		{{range .History}}
			<tr>
                <td>{{.StartTime.Format "2006-01-02 15:04:05"}}</td>
				<td>{{.Status.HTMLTableString}}</td>
				{{range .Details}}
					<td>{{.Status.HTMLTableString}} </td>
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

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
)

func (s RunStatus) String() string {
	switch s {
	case RunSuccess:
		return "success"
	case RunFailure:
		return "failure"
	default:
		return "unknown"
	}
}

func (s RunStatus) HTMLTableString() string {
	switch s {
	case RunSuccess:
		return "s"
	case RunFailure:
		return "f"
	default:
		return "?"
	}
}

type Task struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type TaskRun struct {
	Name      string
	Cmd       string
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
}

type TasksSequenceRun struct {
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
	Details   []*TaskRun
}

type TasksSequence struct {
	Title       string  `json:"title"`
	Cron        string  `json:"cron"`
	Tasks       []*Task `json:"tasks"`
	cronID      cron.EntryID
	cronJobFunc cron.FuncJob
	History     []*TasksSequenceRun
}

type AMessOfTasks struct {
	Tasks []*TasksSequence
}

func main() {
	var tasks AMessOfTasks
	c := cron.New(cron.WithSeconds())
	c.Start()
	//scanTasksSchedule := "*/10 * * * * *"
	//dirScanCronJobFunc := func() { scanTasks(&tasks) }
	//c.AddFunc(scanTasksSchedule, dirScanCronJobFunc)
	scanTasks(&tasks)
	runTasks(&tasks, c)
	webServer(&tasks)
}

func webServer(tasks *AMessOfTasks) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		listTasks(w, tasks)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func listTasks(w http.ResponseWriter, tasks *AMessOfTasks) {
	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webTasksList))
	err := tmpl.Execute(w, tasks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func scanTasks(tasks *AMessOfTasks) {
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

func processJSONFile(filePath string) (*TasksSequence, error) {
	var tseq TasksSequence
	jsonFile, err := os.ReadFile(filePath)
	if err != nil {
		slog.Error("Error reading JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	err = json.Unmarshal(jsonFile, &tseq)
	if err != nil {
		slog.Error("Error parsing JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	return &tseq, nil
}

func addTask(path string, tseq *TasksSequence, tasks *AMessOfTasks) {
	for _, existing := range tasks.Tasks {
		if existing.Title == tseq.Title {
			slog.Warn("Task with title", tseq.Title, "already exists, skipping processing.")
			return
		}
	}
	tasks.Tasks = append(tasks.Tasks, tseq)
	slog.Info("Added task", tseq.Title, "from file", path)
}

func runTasks(tasks *AMessOfTasks, c *cron.Cron) {
	for _, t := range tasks.Tasks {
		t := t // Capture variable
		t.cronJobFunc = func() { runTaskCommands(t) }
		t.cronID, _ = c.AddFunc(t.Cron, t.cronJobFunc)
	}
}

func runTaskCommands(tseq *TasksSequence) {
	slog.Info("Running", tseq.Title)

	run := &TasksSequenceRun{StartTime: time.Now()}
	defer func() {
		run.EndTime = time.Now()
		//append to front to simplify web output
		tseq.History = append([]*TasksSequenceRun{run}, tseq.History...)
	}()

	for _, c := range tseq.Tasks {
		cmdStartTime := time.Now()
		output, err := executeCommand(c.Cmd)
		cmdEndTime := time.Now()
		cmdStatus := RunSuccess
		if err != nil {
			slog.Error("Error executing '%s'-'%s': %v\n", tseq.Title, c.Name, err)
			cmdStatus = RunFailure
			run.Status = RunFailure
			return
		}
		slog.Info("Task", tseq.Title, "command", c.Name, "output", output)

		run.Details = append(run.Details, &TaskRun{
			Name:      c.Name,
			Cmd:       c.Cmd,
			StartTime: cmdStartTime,
			EndTime:   cmdEndTime,
			Status:    cmdStatus,
		})
	}
	run.Status = RunSuccess
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
