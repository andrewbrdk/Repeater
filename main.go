package main

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gookit/slog"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const tasksDir = "./"
const scanSchedule = "*/10 * * * * *"

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
	Running
	NoRun
)

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
	c.AddFunc(
		scanSchedule,
		func() { scanAndScheduleTasks(&tasks, c) },
	)
	c.Start()
	httpServer(&tasks)
}

func scanAndScheduleTasks(tasks *AMessOfTasks, c *cron.Cron) {
	files := make(map[string][16]byte)
	err := scanFiles(files)
	if err != nil {
		slog.Errorf("Errors while reading files")
	}
	for _, tseq := range tasks.Tasks {
		md5, haskey := files[tseq.File]
		if !haskey {
			slog.Infof("deleting %s", tseq.Title)
			//err := delete(tseq)
		} else if md5 != tseq.MD5 {
			slog.Infof("file %s has changed, reloading", tseq.File)
			//err := reload(tseq.File)
			delete(files, tseq.File)
		} else if md5 == tseq.MD5 {
			slog.Infof("file %s has not changed, skipping", tseq.File)
			delete(files, tseq.File)
		} else {
			panic("This is not supposed to happen")
		}
	}
	for f := range files {
		slog.Infof("loading %s", f)
		tseq, _ := processTasksFile(f)
		//if err != nil { }
		scheduleTasks(tseq, tasks, c)
	}
}

func scanFiles(files map[string][16]byte) error {
	dir := tasksDir
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		//todo: dont return on error,
		//continue with other files
		if err != nil {
			slog.Errorf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".tasks" {
			f, err := os.ReadFile(path)
			if err != nil {
				slog.Errorf("Error reading file %s: %v\n", path, err)
				return err
			}
			files[path] = md5.Sum(f)
		}
		return nil
	})
	return err
}

func processTasksFile(filePath string) (*TasksSequence, error) {
	var tseq TasksSequence
	tseq.File = filePath
	tseq.OnOff = true
	f, err := os.ReadFile(filePath)
	if err != nil {
		slog.Errorf("Error reading file %s: %v\n", filePath, err)
		return nil, err
	}
	tseq.MD5 = md5.Sum(f)
	err = json.Unmarshal(f, &tseq)
	if err != nil {
		slog.Errorf("Error parsing file %s: %v\n", filePath, err)
		return nil, err
	}
	return &tseq, nil
}

func scheduleTasks(tseq *TasksSequence, tasks *AMessOfTasks, c *cron.Cron) {
	tasks.Tasks = append(tasks.Tasks, tseq)
	tseq.cronID, _ = c.AddFunc(
		tseq.Cron,
		func() { runTaskCommands(tseq) },
	)
	slog.Infof("Added TasksSequence '%s' from file '%s'", tseq.Title, tseq.File)
}

func runTaskCommands(tseq *TasksSequence) {
	if !tseq.OnOff {
		slog.Infof("Skipping '%s'", tseq.Title)
		return
	}
	run := initRun(tseq)
	tseq.History = append(tseq.History, run)
	run.Status = Running
	slog.Infof("Running '%s'", tseq.Title)

	var taskFail bool
	for _, tr := range run.Details {
		err := cmdRun(tr, tseq.Title)
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
	run.EndTime = time.Now()
}

func initRun(tseq *TasksSequence) *TasksSequenceRun {
	run := &TasksSequenceRun{
		ID:        uuid.New().String(),
		StartTime: time.Now(),
	}
	for _, c := range tseq.Tasks {
		run.Details = append(run.Details, &TaskRun{
			ID:     uuid.New().String(),
			Name:   c.Name,
			Cmd:    c.Cmd,
			Status: NoRun,
		})
	}
	return run
}

func cmdRun(tr *TaskRun, title string) error {
	tr.StartTime = time.Now()
	output, err := executeCommand(tr.Cmd)
	tr.EndTime = time.Now()
	tr.Status = RunSuccess
	if err != nil {
		slog.Errorf("Error executing '%s'-'%s': %v\n", title, tr.Name, err)
		tr.Status = RunFailure
	}
	slog.Infof("Task '%s', command '%s', output: '%s'\n", title, tr.Name, output)
	return err
}

func executeCommand(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
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

func sequenceOnOff(taskidx int, tasks *AMessOfTasks) error {
	if taskidx >= len(tasks.Tasks) || taskidx < 0 {
		slog.Errorf("incorrect task index %v", taskidx)
		return errors.New("incorrect task index")
	}
	ts := tasks.Tasks[taskidx]
	ts.OnOff = !ts.OnOff
	slog.Infof("Toggled state of %s to %v", ts.Title, ts.OnOff)
	return nil
}

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
    {{range $i, $t := .Mess.Tasks}}
    <div>
    <details open>
	<summary>
		<strong>{{.Title}}</strong>
		<span>{{.Cron}}</span>
    	<button onclick="onoff( {{$i}} )">{{if .OnOff}}Turn Off{{else}}Turn On{{end}}</button>
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
        function onoff(taskidx) {
            fetch('/onoff?taskidx=' + taskidx)
                .then(response => {
                    location.reload();
                })
                .catch(error => {
                    console.error('Error toggling state:', error);
                });
        }
        function restartTask(uuid) {
            fetch('/restart?uuid=' + uuid)
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

type HTMLTemplateData struct {
	Mess *AMessOfTasks
}

func (s RunStatus) HTMLStatus() template.HTML {
	switch s {
	case RunSuccess:
		//return "&#9632;"
		return "■"
	case RunFailure:
		//return "&Cross;"
		return "⨯"
	case Running:
		//return "&#9704"
		return "◨"
	case NoRun:
		//return &#9633;
		return "□"
	default:
		return "?"
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

func httpServer(tasks *AMessOfTasks) {
	template_data := &HTMLTemplateData{Mess: tasks}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpListTasks(w, r, template_data)
	})
	http.HandleFunc("/onoff", func(w http.ResponseWriter, r *http.Request) {
		httpOnOff(w, r, template_data)
	})
	http.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		httpRestart(w, r, template_data)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func httpListTasks(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	uuid := r.URL.Query().Get("uuid")

	for _, taskSeq := range template_data.Mess.Tasks {
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
	err := tmpl.Execute(w, template_data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func httpOnOff(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	taskidx_str := r.URL.Query().Get("taskidx")
	taskidx, err := strconv.Atoi(taskidx_str)
	if err != nil {
		slog.Errorf("error converting taskidx string %s to int", taskidx_str)
		http.Error(w, "TasksSequence not found", http.StatusNotFound)
		return
	}
	err = sequenceOnOff(taskidx, template_data.Mess)
	if err != nil {
		http.Error(w, "TasksSequence not found", http.StatusNotFound)
	}
}

func httpRestart(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	uuid := r.URL.Query().Get("uuid")
	if uuid == "" {
		http.Error(w, "UUID parameter is required", http.StatusBadRequest)
		return
	}

	for _, taskSeq := range template_data.Mess.Tasks {
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
