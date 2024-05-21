package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gookit/slog"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const tasksDir = "./"
const scanTasksSchedule = "*/10 * * * * *"

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
	<summary>
		<strong>{{.Title}}</strong>
		<span>{{.Cron}}</span>
    	<button onclick="toggleState('{{.Title}}')">{{if .OnOff}}Turn Off{{else}}Turn On{{end}}</button>
	</summary>
    <div style="overflow-x:auto;">
    {{.HTMLHistoryTable}}
    </div>
    {{if .ShowRestartButton}}
    <button onclick="restartTask('{{.RestartUUID}}')">Restart</button>
    {{end}}
    </details>
    </div>
    {{end}}
    <script>
        function toggleState(title) {
            fetch('/toggle-state?title=' + title)
                .then(response => {
                    location.reload();
                })
                .catch(error => {
                    console.error('Error toggling state:', error);
                });
        }
        function restartTask(uuid) {
            fetch('/restart-task?uuid=' + uuid)
                .then(response => {
                    location.reload();
                })
                .catch(error => {
                    console.error('Error restarting task:', error);
                });
        }
    </script>
</body>
</html>
`

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
	NoRun
)

func (s RunStatus) String() string {
	switch s {
	case RunSuccess:
		return "success"
	case RunFailure:
		return "failure"
	case NoRun:
		return "no run"
	default:
		return "unknown"
	}
}

func (s RunStatus) HTMLStatus() template.HTML {
	switch s {
	case RunSuccess:
		//return "&#9632;"
		return "■"
	case RunFailure:
		//return "&Cross;"
		return "⨯"
	case NoRun:
		//return &#9633;
		return "□"
	default:
		return "?"
	}
}

type Task struct {
	Name string `json:"name"`
	Cmd  string `json:"cmd"`
}

type TaskRun struct {
	ID        string
	Name      string
	Cmd       string
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
}

type TasksSequenceRun struct {
	ID        string
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
	Details   []*TaskRun
}

type TasksSequence struct {
	File              string
	MD5               [16]byte
	Title             string  `json:"title"`
	Cron              string  `json:"cron"`
	Tasks             []*Task `json:"tasks"`
	cronID            cron.EntryID
	cronJobFunc       cron.FuncJob
	History           []*TasksSequenceRun
	OnOff             bool
	ShowRestartButton bool
	RestartUUID       string
}

type AMessOfTasks struct {
	Tasks []*TasksSequence
}

func main() {
	var tasks AMessOfTasks
	c := cron.New(cron.WithSeconds())
	c.Start()
	dirScanCronJobFunc := func() { scanAndScheduleTasks(&tasks, c) }
	c.AddFunc(scanTasksSchedule, dirScanCronJobFunc)
	webServer(&tasks)
}

func webServer(tasks *AMessOfTasks) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		listTasks(w, r, tasks)
	})
	http.HandleFunc("/toggle-state", func(w http.ResponseWriter, r *http.Request) {
		toggleStateHandler(w, r, tasks)
	})
	http.HandleFunc("/restart-task", func(w http.ResponseWriter, r *http.Request) {
		restartTaskHandler(w, r, tasks)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func listTasks(w http.ResponseWriter, r *http.Request, tasks *AMessOfTasks) {
	uuid := r.URL.Query().Get("uuid")

	for _, taskSeq := range tasks.Tasks {
		taskSeq.ShowRestartButton = false
		for _, seqRun := range taskSeq.History {
			if seqRun.ID == uuid {
				taskSeq.ShowRestartButton = true
				taskSeq.RestartUUID = uuid
			}
			for _, taskRun := range seqRun.Details {
				if taskRun.ID == uuid {
					taskSeq.ShowRestartButton = true
					taskSeq.RestartUUID = uuid
				}
			}
		}
	}

	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webTasksList))
	err := tmpl.Execute(w, tasks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (tseq TasksSequence) HTMLHistoryTable() template.HTML {
	var sb strings.Builder
	sb.WriteString("<table>\n")
	for r := -1; r < len(tseq.Tasks); r++ {
		sb.WriteString("<tr>\n")
		for c := -1; c <= len(tseq.History); c++ {
			if r == -1 && c == -1 {
				sb.WriteString("<th> </th>")
			} else if r == -1 && c < len(tseq.History) {
				runID := tseq.History[c].ID
				sb.WriteString(fmt.Sprintf("<th> <a href=\"/?uuid=%s\">%s</a> </th>", runID, tseq.History[c].Status.HTMLStatus()))
			} else if r == -1 && c == len(tseq.History) {
				sb.WriteString("<th>&#9633;</th>")
			} else if c == -1 {
				sb.WriteString(fmt.Sprintf("<td> %s </td>", html.EscapeString(tseq.Tasks[r].Name)))
			} else if c < len(tseq.History) {
				taskRunID := tseq.History[c].Details[r].ID
				sb.WriteString(fmt.Sprintf("<td> <a href=\"/?uuid=%s\">%s</a> </td>", taskRunID, tseq.History[c].Details[r].Status.HTMLStatus()))
			} else if c == len(tseq.History) {
				sb.WriteString("<td>&#9633;</td>")
			} else {
				slog.Error("this is not supposed to happen")
			}
		}
		sb.WriteString("</tr>\n")
	}
	sb.WriteString("</table>\n")
	return template.HTML(sb.String())
}

func toggleStateHandler(w http.ResponseWriter, r *http.Request, tasks *AMessOfTasks) {
	title := r.FormValue("title")

	for _, taskSeq := range tasks.Tasks {
		if taskSeq.Title == title {
			taskSeq.OnOff = !taskSeq.OnOff
			slog.Infof("Toggled state of %s to %v", title, taskSeq.OnOff)
			return
		}
	}
	http.Error(w, "TasksSequence not found", http.StatusNotFound)
}

func scanAndScheduleTasks(tasks *AMessOfTasks, c *cron.Cron) {
	dir := tasksDir
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Errorf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".tasks" {
			tseq, err := processJSONFile(path)
			if err != nil {
				return err
			}
			addAndScheduleTasks(tseq, tasks, c)
		}
		return nil
	})
	if err != nil {
		slog.Fatal(err)
	}
}

func processJSONFile(filePath string) (*TasksSequence, error) {
	var tseq TasksSequence
	tseq.File = filePath
	tseq.OnOff = true
	jsonFile, err := os.ReadFile(filePath)
	if err != nil {
		slog.Errorf("Error reading JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	tseq.MD5 = md5.Sum(jsonFile)
	err = json.Unmarshal(jsonFile, &tseq)
	if err != nil {
		slog.Errorf("Error parsing JSON file %s: %v\n", filePath, err)
		return nil, err
	}
	return &tseq, nil
}

func addAndScheduleTasks(tseq *TasksSequence, tasks *AMessOfTasks, c *cron.Cron) {
	for _, existing := range tasks.Tasks {
		if existing.File == tseq.File && existing.MD5 == tseq.MD5 {
			slog.Infof("File '%s' already loaded, skipping.", tseq.File)
			return
		}
	}
	tasks.Tasks = append(tasks.Tasks, tseq)
	slog.Infof("Added TasksSequence '%s' from file '%s'", tseq.Title, tseq.File)
	tseq.cronJobFunc = func() { runTaskCommands(tseq) }
	tseq.cronID, _ = c.AddFunc(tseq.Cron, tseq.cronJobFunc)
}

func runTaskCommands(tseq *TasksSequence) {
	if !tseq.OnOff {
		slog.Infof("Skipping '%s'", tseq.Title)
		return
	}
	slog.Infof("Running '%s'", tseq.Title)

	run := &TasksSequenceRun{
		ID:        uuid.New().String(),
		StartTime: time.Now(),
	}
	defer func() {
		run.EndTime = time.Now()
		tseq.History = append(tseq.History, run)
	}()
	for _, c := range tseq.Tasks {
		run.Details = append(run.Details, &TaskRun{
			ID:     uuid.New().String(),
			Name:   c.Name,
			Cmd:    c.Cmd,
			Status: NoRun,
		})
	}

	var taskFail bool
	for i, c := range tseq.Tasks {
		cmdStartTime := time.Now()
		output, err := executeCommand(c.Cmd)
		cmdEndTime := time.Now()
		cmdStatus := RunSuccess
		if err != nil {
			slog.Errorf("Error executing '%s'-'%s': %v\n", tseq.Title, c.Name, err)
			cmdStatus = RunFailure
			run.Status = RunFailure
		}
		slog.Infof("Task '%s', command '%s', output: '%s'\n", tseq.Title, c.Name, output)
		run.Details[i].StartTime = cmdStartTime
		run.Details[i].EndTime = cmdEndTime
		run.Details[i].Status = cmdStatus
		if err != nil {
			taskFail = true
			slog.Errorf("Should be skipping to next task")
			break
		}
	}
	run.Status = RunSuccess
	if taskFail {
		run.Status = RunFailure
	}

}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func restartTaskHandler(w http.ResponseWriter, r *http.Request, tasks *AMessOfTasks) {
	uuid := r.URL.Query().Get("uuid")
	if uuid == "" {
		http.Error(w, "UUID parameter is required", http.StatusBadRequest)
		return
	}

	for _, taskSeq := range tasks.Tasks {
		for _, seqRun := range taskSeq.History {
			if seqRun.ID == uuid {
				restartTaskSequenceRun(taskSeq, seqRun)
				return
			}
			for _, taskRun := range seqRun.Details {
				if taskRun.ID == uuid {
					restartSpecificTaskRun(taskSeq, taskRun)
					return
				}
			}
		}
	}
	http.Error(w, "TaskSequenceRun or TaskRun not found", http.StatusNotFound)
}

func restartTaskSequenceRun(tseq *TasksSequence, seqRun *TasksSequenceRun) {
	slog.Infof("Restarting TaskSequenceRun %s", seqRun.ID)

	newSeqRun := &TasksSequenceRun{ID: uuid.New().String(), StartTime: time.Now()}
	defer func() {
		newSeqRun.EndTime = time.Now()
		tseq.History = append(tseq.History, newSeqRun)
	}()

	for _, taskRun := range seqRun.Details {
		newTaskRun := &TaskRun{
			ID:        uuid.New().String(),
			Name:      taskRun.Name,
			Cmd:       taskRun.Cmd,
			StartTime: time.Now(),
		}

		output, err := executeCommand(taskRun.Cmd)
		newTaskRun.EndTime = time.Now()
		if err != nil {
			slog.Errorf("Error executing '%s'-'%s': %v\n", tseq.Title, taskRun.Name, err)
			newTaskRun.Status = RunFailure
			newSeqRun.Status = RunFailure
		} else {
			newTaskRun.Status = RunSuccess
		}

		slog.Infof("Task '%s', command '%s', output: '%s'\n", tseq.Title, taskRun.Name, output)
		newSeqRun.Details = append(newSeqRun.Details, newTaskRun)
	}

	if newSeqRun.Status != RunFailure {
		newSeqRun.Status = RunSuccess
	}
}

func restartSpecificTaskRun(tseq *TasksSequence, taskRun *TaskRun) {
	slog.Infof("Restarting TaskRun %s", taskRun.ID)

	cmdStartTime := time.Now()
	output, err := executeCommand(taskRun.Cmd)
	EndTime := time.Now()
	Status := RunSuccess
	if err != nil {
		slog.Errorf("Error executing '%s'-'%s': %v\n", tseq.Title, taskRun.Name, err)
		Status = RunFailure
	}

	slog.Infof("Task '%s', command '%s', output: '%s'\n", tseq.Title, taskRun.Name, output)

	for _, seqRun := range tseq.History {
		for i, existingTaskRun := range seqRun.Details {
			if existingTaskRun.ID == taskRun.ID {
				seqRun.Details[i].StartTime = cmdStartTime
				seqRun.Details[i].EndTime = EndTime
				seqRun.Details[i].Status = Status
				return
			}
		}
	}
}
