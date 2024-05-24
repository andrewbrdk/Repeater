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
	Name      string
	Cmd       string
	StartTime time.Time
	EndTime   time.Time
	Status    RunStatus
	Attempt   int
}

type TasksSequenceRun struct {
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
		func() { runScheduled(tseq) },
	)
	slog.Infof("Added TasksSequence '%s' from file '%s'", tseq.Title, tseq.File)
}

func runScheduled(tseq *TasksSequence) {
	if !tseq.OnOff {
		slog.Infof("Skipping '%s'", tseq.Title)
		return
	}
	run := initRun(tseq)
	runSequence(run, tseq)
	//todo: check for error
}

func initRun(tseq *TasksSequence) *TasksSequenceRun {
	run := &TasksSequenceRun{
		StartTime: time.Now(),
	}
	for _, c := range tseq.Tasks {
		run.Details = append(run.Details, &TaskRun{
			Name:    c.Name,
			Cmd:     c.Cmd,
			Status:  NoRun,
			Attempt: 0,
		})
	}
	tseq.History = append(tseq.History, run)
	return run
}

func runSequence(run *TasksSequenceRun, tseq *TasksSequence) error {
	run.Status = Running
	slog.Infof("Running '%s'", tseq.Title)

	var taskFail bool
	for _, tr := range run.Details {
		err := runCommand(tr, tseq.Title)
		if err != nil {
			taskFail = true
			break
		}
	}
	run.Status = RunSuccess
	if taskFail {
		run.Status = RunFailure
	}
	run.EndTime = time.Now()
	//todo: return error
	return nil
}

func runCommand(tr *TaskRun, title string) error {
	tr.StartTime = time.Now()
	output, err := executeCmd(tr.Cmd)
	tr.EndTime = time.Now()
	tr.Attempt += tr.Attempt
	tr.Status = RunSuccess
	if err != nil {
		slog.Errorf("Error executing '%s'-'%s': %v\n", title, tr.Name, err)
		tr.Status = RunFailure
	}
	slog.Infof("Task '%s', command '%s', output: '%s'\n", title, tr.Name, output)
	return err
}

func executeCmd(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func restartTaskSequenceRun(tseq *TasksSequence, seqRun *TasksSequenceRun) {
	seqRun.Status = NoRun
	for _, r := range seqRun.Details {
		r.Status = NoRun
	}
	runSequence(seqRun, tseq)
}

func restartTaskRun(tseq *TasksSequence, taskRun *TaskRun) {
	runCommand(taskRun, tseq.Title)
	//todo: add error check
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
    <button onclick="restart('{{.RestartUUID}}')">Restart</button>
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
        function restart(uuid) {
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
	run_idx int
	cmd_idx int
	Mess    *AMessOfTasks
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
				sb.WriteString(fmt.Sprintf("<th> <a href=\"/?run=%v\">%s</a> </th>", c, tseq.History[c].Status.HTMLStatus()))
			} else if r == -1 && c == len(tseq.History) {
				sb.WriteString("<th>&#9633;</th>")
			} else if c == -1 {
				sb.WriteString(fmt.Sprintf("<td> %s </td>", html.EscapeString(tseq.Tasks[r].Name)))
			} else if c < len(tseq.History) {
				sb.WriteString(fmt.Sprintf("<td> <a href=\"/?run=%v&cmd=%v\">%s</a> </td>", c, r, tseq.History[c].Details[r].Status.HTMLStatus()))
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
	run_str := r.URL.Query().Get("run")
	cmd_str := r.URL.Query().Get("cmd")
	run, err := strconv.Atoi(run_str)
	if err != nil {
		slog.Errorf("error converting run string %s to int", run_str)
	}
	cmd, err := strconv.Atoi(cmd_str)
	if err != nil {
		slog.Errorf("error converting cmd string %s to int", cmd_str)
	}
	template_data.run_idx = run
	template_data.cmd_idx = cmd

	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webTasksList))
	err = tmpl.Execute(w, template_data)
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
		http.Error(w, "taskidx not found", http.StatusNotFound)
		return
	}
	err = sequenceOnOff(taskidx, template_data.Mess)
	if err != nil {
		http.Error(w, "TasksSequence not found", http.StatusNotFound)
	}
}

func httpRestart(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	task_str := r.URL.Query().Get("task")
	run_str := r.URL.Query().Get("run")
	cmd_str := r.URL.Query().Get("cmd")
	task, err := strconv.Atoi(task_str)
	if err != nil {
		slog.Errorf("error converting task string %s to int", task_str)
		return
	}
	run, err := strconv.Atoi(run_str)
	if err != nil {
		slog.Errorf("error converting run string %s to int", run_str)
		return
	}
	cmd, err := strconv.Atoi(cmd_str)
	if err != nil {
		slog.Errorf("error converting cmd string %s to int", cmd_str)
		return
	}

	t := template_data.Mess.Tasks[task]
	rn := t.History[run]
	c := rn.Details[cmd]
	if rn != nil && c != nil {
		restartTaskRun(t, c)
	} else if rn != nil {
		restartTaskSequenceRun(t, rn)
	} else {
		http.Error(w, "TaskSequenceRun or TaskRun not found", http.StatusNotFound)
	}
}
