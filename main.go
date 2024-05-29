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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/slog"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const tasksDir = "./"
const scanSchedule = "*/10 * * * * *"
const HTMLTitle = "Repeater"

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
	File    string
	MD5     [16]byte
	Title   string  `json:"title"`
	Cron    string  `json:"cron"`
	Tasks   []*Task `json:"tasks"`
	cronID  cron.EntryID
	History []*TasksSequenceRun
	OnOff   bool
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
	var todelete []int
	files := make(map[string][16]byte)
	err := scanFiles(files)
	if err != nil {
		slog.Errorf("Errors while reading files")
	}
	for idx, tseq := range tasks.Tasks {
		md5, haskey := files[tseq.File]
		if !haskey {
			slog.Infof("marking %s for deletion", tseq.Title)
			todelete = append(todelete, idx)
		} else if md5 != tseq.MD5 {
			slog.Infof("file %s has changed, marking for reloading", tseq.File)
			todelete = append(todelete, idx)
		} else if md5 == tseq.MD5 {
			slog.Infof("file %s has not changed, skipping", tseq.File)
			delete(files, tseq.File)
		} else {
			panic("This is not supposed to happen")
		}
	}
	//todo: simplify removing
	if len(todelete) > 0 {
		sort.Sort(sort.Reverse(sort.IntSlice(todelete)))
		last_idx := len(tasks.Tasks) - 1
		for _, task_idx := range todelete {
			c.Remove(tasks.Tasks[task_idx].cronID)
			tasks.Tasks[task_idx] = tasks.Tasks[last_idx]
			last_idx = last_idx - 1
		}
		if last_idx >= 0 {
			tasks.Tasks = tasks.Tasks[:last_idx]
		} else {
			tasks.Tasks = nil
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
	tr.Status = Running
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
    <title>{{.Title}}</title>
</head>
<body>
    {{.HTMLListTasks}}
    <script>
        function onoff(task) {
            fetch('/onoff?task=' + task)
                .then(response => {
                    location.reload();
                })
                .catch(error => {
                    console.error('Error toggling state:', error);
                });
        }
        function restart(task, run, cmd) {
            fetch('/restart?task=' + task + '&run=' + run + '&cmd=' + cmd)
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
	task_idx int
	run_idx  int
	cmd_idx  int
	Title    string
	Mess     *AMessOfTasks
}

func (td HTMLTemplateData) HTMLListTasks() template.HTML {
	var sb strings.Builder
	var btn_text string
	sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", td.Title))
	for i, tseq := range td.Mess.Tasks {
		sb.WriteString("<div>")
		sb.WriteString("<details open>")
		sb.WriteString("<summary>")
		sb.WriteString(fmt.Sprintf("<strong>%s</strong>", tseq.Title))
		sb.WriteString(fmt.Sprintf("<span>%s</span>", tseq.Cron))
		if tseq.OnOff {
			btn_text = "Turn Off"
		} else {
			btn_text = "Turn On"
		}
		sb.WriteString(fmt.Sprintf("<button onclick=\"onoff( %v )\">%s</button>", i, btn_text))
		sb.WriteString("</summary>")
		sb.WriteString("<div style=\"overflow-x:auto;\">")
		sb.WriteString(td.HTMLHistoryTable(i))
		sb.WriteString("</div>")
		if td.task_idx == i {
			sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart</button>", td.task_idx, td.run_idx, td.cmd_idx))
		}
		sb.WriteString("</details>")
		sb.WriteString("</div>")
	}
	return template.HTML(sb.String())
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

func (td HTMLTemplateData) HTMLHistoryTable(task_idx int) string {
	tseq := td.Mess.Tasks[task_idx]
	var sb strings.Builder
	sb.WriteString("<table>\n")
	for r := -1; r < len(tseq.Tasks); r++ {
		sb.WriteString("<tr>\n")
		for c := -1; c <= len(tseq.History); c++ {
			if r == -1 && c == -1 {
				sb.WriteString("<th> </th>")
			} else if r == -1 && c < len(tseq.History) {
				sb.WriteString(fmt.Sprintf("<th> <a href=\"/?task=%v&run=%v\">%s</a> </th>", task_idx, c, tseq.History[c].Status.HTMLStatus()))
			} else if r == -1 && c == len(tseq.History) {
				sb.WriteString("<th>&#9633;</th>")
			} else if c == -1 {
				sb.WriteString(fmt.Sprintf("<td> %s </td>", html.EscapeString(tseq.Tasks[r].Name)))
			} else if c < len(tseq.History) {
				sb.WriteString(fmt.Sprintf("<td> <a href=\"/?task=%v&run=%v&cmd=%v\">%s</a> </td>", task_idx, c, r, tseq.History[c].Details[r].Status.HTMLStatus()))
			} else if c == len(tseq.History) {
				sb.WriteString("<td>&#9633;</td>")
			} else {
				slog.Error("this is not supposed to happen")
			}
		}
		sb.WriteString("</tr>\n")
	}
	sb.WriteString("</table>\n")
	return sb.String()
}

func httpServer(tasks *AMessOfTasks) {
	template_data := &HTMLTemplateData{Mess: tasks, Title: HTMLTitle}
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
	httpParseTaskRunCmd(r, template_data)
	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webTasksList))
	err := tmpl.Execute(w, template_data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func httpOnOff(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	httpParseTaskRunCmd(r, template_data)
	err := sequenceOnOff(template_data.task_idx, template_data.Mess)
	if err != nil {
		http.Error(w, "TasksSequence not found", http.StatusNotFound)
	}
}

func httpRestart(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	httpParseTaskRunCmd(r, template_data)
	var t *TasksSequence
	t = nil
	if template_data.task_idx != -1 {
		t = template_data.Mess.Tasks[template_data.task_idx]
	}
	var rn *TasksSequenceRun
	rn = nil
	if t != nil && template_data.run_idx != -1 {
		rn = t.History[template_data.run_idx]
	}
	var c *TaskRun
	c = nil
	if rn != nil && template_data.cmd_idx != -1 {
		c = rn.Details[template_data.cmd_idx]
	}
	if rn != nil && c != nil {
		restartTaskRun(t, c)
	} else if rn != nil {
		restartTaskSequenceRun(t, rn)
	} else {
		http.Error(w, "TaskSequenceRun or TaskRun not found", http.StatusNotFound)
	}
}

func httpParseTaskRunCmd(r *http.Request, template_data *HTMLTemplateData) {
	task_str := r.URL.Query().Get("task")
	task, err := strconv.Atoi(task_str)
	if err != nil {
		task = -1
	} else if task < 0 || task > len(template_data.Mess.Tasks) {
		task = -1
	}
	template_data.task_idx = task
	run_str := r.URL.Query().Get("run")
	run, err := strconv.Atoi(run_str)
	if err != nil {
		run = -1
	} else if task != -1 && (run < 0 || run > len(template_data.Mess.Tasks[task].History)) {
		run = -1
	}
	template_data.run_idx = run
	cmd_str := r.URL.Query().Get("cmd")
	cmd, err := strconv.Atoi(cmd_str)
	if err != nil {
		cmd = -1
	} else if task != -1 && run != -1 && (cmd < 0 || cmd > len(template_data.Mess.Tasks[task].History[run].Details)) {
		cmd = -1
	}
	template_data.cmd_idx = cmd
}
