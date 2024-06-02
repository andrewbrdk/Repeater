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
	texttemplate "text/template"
	"time"

	"github.com/gookit/slog"
	hcron "github.com/lnquy/cron"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const tasksDir = "./"
const scanSchedule = "*/10 * * * * *"
const htmlTitle = "Repeater"

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
	Name        string
	Cmd         string
	RenderedCmd string
	StartTime   time.Time
	EndTime     time.Time
	Status      RunStatus
	Attempt     int
	//todo: pass only necessary parameters to tasks
	SequenceRun *TasksSequenceRun
	//todo: store in db?
	LastOutput string
}

type TasksSequenceRun struct {
	ScheduledTime time.Time
	StartTime     time.Time
	EndTime       time.Time
	Status        RunStatus
	Details       []*TaskRun
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
		func() { runScheduled(tseq, c) },
	)
	slog.Infof("Added TasksSequence '%s' from file '%s'", tseq.Title, tseq.File)
}

func runScheduled(tseq *TasksSequence, c *cron.Cron) {
	//todo: don't pass *cron.Cron
	if !tseq.OnOff {
		slog.Infof("Skipping '%s'", tseq.Title)
		return
	}
	run := initRun(tseq, c)
	runSequence(run, tseq)
	//todo: check for errors
}

func initRun(tseq *TasksSequence, c *cron.Cron) *TasksSequenceRun {
	run := &TasksSequenceRun{
		//todo: get scheduled run time for the job
		ScheduledTime: c.Entry(tseq.cronID).Prev,
		StartTime:     time.Now(),
	}
	for _, t := range tseq.Tasks {
		run.Details = append(run.Details, &TaskRun{
			Name:    t.Name,
			Cmd:     t.Cmd,
			Status:  NoRun,
			Attempt: 0,
			//todo: pass only necessary parameters to tasks
			SequenceRun: run,
		})
	}
	tseq.History = append(tseq.History, run)
	return run
}

func runSequence(run *TasksSequenceRun, tseq *TasksSequence) error {
	run.Status = Running
	slog.Infof("Running '%s'", tseq.Title)
	template_data := make(map[string]string)
	template_data["title"] = tseq.Title
	template_data["scheduled_dt"] = run.ScheduledTime.Format("2006-01-02")
	var taskFail bool
	for _, tr := range run.Details {
		err := runCommand(tr, template_data)
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

func runCommand(tr *TaskRun, template_data map[string]string) error {
	tmpl := texttemplate.New("tmpl")
	tmpl, err := tmpl.Parse(tr.Cmd)
	if err != nil {
		slog.Errorf("Error parsing command template '%s'-'%s'-'%s': %v\n", template_data["title"], tr.Name, tr.Cmd, err)
		return err
	}
	sb := new(strings.Builder)
	err = tmpl.Execute(sb, template_data)
	if err != nil {
		slog.Errorf("Error rendering command template '%s'-'%s'-'%s': %v\n", template_data["title"], tr.Name, tr.Cmd, err)
		return err
	}
	tr.StartTime = time.Now()
	tr.Attempt += tr.Attempt
	tr.RenderedCmd = sb.String()
	tr.Status = Running
	output, err := executeCmd(tr.RenderedCmd)
	tr.LastOutput = output
	tr.EndTime = time.Now()
	tr.Status = RunSuccess
	if err != nil {
		slog.Errorf("Error executing '%s'-'%s': %v\n", template_data["title"], tr.Name, err)
		tr.Status = RunFailure
	}
	return err
}

func executeCmd(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func restartTaskSequenceRun(tseq *TasksSequence, seqRun *TasksSequenceRun) {
	seqRun.Status = NoRun
	for _, r := range seqRun.Details {
		r.Status = NoRun
	}
	runSequence(seqRun, tseq)
}

func restartTaskRun(tseq *TasksSequence, taskRun *TaskRun) {
	template_data := make(map[string]string)
	template_data["title"] = tseq.Title
	template_data["scheduled_dt"] = taskRun.SequenceRun.ScheduledTime.Format("2006-01-02")
	runCommand(taskRun, template_data)
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

func (tseq TasksSequence) CountFailed() int {
	//todo: maintain counter?
	f := 0
	for _, h := range tseq.History {
		if h.Status == RunFailure {
			f += 1
		}
	}
	return f
}

const webTasksList = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
	<style>
		body {
			margin-left: 10%;
			margin-right: 10%;
		}
        h1 {
            text-align: center;
        }
		details {
            margin-bottom: 20px;
        }
		summary {
			font-size: 1.2em;
			text-align: left;
			margin-bottom: 10px;
		}
		summary > span {
            float:right;
			clear:none;
        }
        summary > span > button {
            margin-left: 20px;
        }
		.history {
			overflow-x:auto;
		}
		table th:first-child, 
		table td:first-child {
			position: sticky;
			left: 0;
			background-color: white;
		}
		details a {
			color: black;
			text-decoration: none;
		}
		pre {
			background-color: #eee;
			font-family: courier, monospace;
			padding: 0 3px;
			display: block;
			font-size: 1.2em;
		}
    </style>
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
		document.addEventListener("DOMContentLoaded", function() {
			for (const e of document.querySelectorAll('.history')) {
				e.scrollLeft = e.scrollWidth;
			}
		});
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
	var cron_text string
	var err error
	exprDesc, _ := hcron.NewDescriptor(hcron.Use24HourTimeFormat(true))
	sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", td.Title))
	for i, tseq := range td.Mess.Tasks {
		sb.WriteString("<div>")
		//todo: display last ten runs in summary instead of fails count
		failed_cnt := tseq.CountFailed()
		if failed_cnt == 0 || !tseq.OnOff {
			sb.WriteString("<details>")
		} else if failed_cnt > 0 && tseq.OnOff {
			sb.WriteString("<details open>")
		}
		sb.WriteString("<summary>")
		sb.WriteString(fmt.Sprintf("<strong>%s</strong>", tseq.Title))
		if failed_cnt > 0 {
			sb.WriteString(fmt.Sprintf("&emsp; %v failed", failed_cnt))
		}
		sb.WriteString("<span>")
		cron_text, err = exprDesc.ToDescription(tseq.Cron, hcron.Locale_en)
		if err != nil {
			cron_text = tseq.Cron
		}
		sb.WriteString(cron_text)
		if tseq.OnOff {
			btn_text = "Turn Off"
		} else {
			btn_text = "Turn On"
		}
		sb.WriteString(fmt.Sprintf("<button onclick=\"onoff( %v )\">%s</button>", i, btn_text))
		sb.WriteString("</span>")
		sb.WriteString("</summary>")
		sb.WriteString("<div class=\"history\">")
		sb.WriteString(td.HTMLHistoryTable(i))
		sb.WriteString("</div>")
		if td.task_idx == i {
			if td.run_idx != -1 {
				run := tseq.History[td.run_idx]
				sb.WriteString("<p>")
				sb.WriteString(fmt.Sprintf("%s: ", run.ScheduledTime.Format("02 Jan 06 15:04")))
				sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart all</button> ", td.task_idx, td.run_idx, td.cmd_idx))
				//sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart failed & dependencies</button>", td.task_idx, td.run_idx, td.cmd_idx))
				sb.WriteString("</p>")
			}
			if td.run_idx != -1 && td.cmd_idx != -1 {
				cmd_run := tseq.History[td.run_idx].Details[td.cmd_idx]
				sb.WriteString("<p>")
				sb.WriteString(fmt.Sprintf("%s: ", cmd_run.Name))
				sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart task</button> ", td.task_idx, td.run_idx, td.cmd_idx))
				//todo: define restart_with_dependencies
				//sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart task & dependencies</button>", td.task_idx, td.run_idx, td.cmd_idx))
				sb.WriteString("</p>")
				sb.WriteString("<pre>")
				sb.WriteString(fmt.Sprintf("<code>> %s </code>\n", cmd_run.RenderedCmd))
				sb.WriteString(fmt.Sprintf("<samp>%s</samp>", cmd_run.LastOutput))
				sb.WriteString("</pre>")
			}
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
	//todo: init once
	template_data := &HTMLTemplateData{Mess: tasks, Title: htmlTitle}
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
	} else if task < 0 || task >= len(template_data.Mess.Tasks) {
		task = -1
	}
	template_data.task_idx = task
	run_str := r.URL.Query().Get("run")
	run, err := strconv.Atoi(run_str)
	if err != nil {
		run = -1
	} else if task != -1 && (run < 0 || run >= len(template_data.Mess.Tasks[task].History)) {
		run = -1
	}
	template_data.run_idx = run
	cmd_str := r.URL.Query().Get("cmd")
	cmd, err := strconv.Atoi(cmd_str)
	if err != nil {
		cmd = -1
	} else if task != -1 && run != -1 && (cmd < 0 || cmd >= len(template_data.Mess.Tasks[task].History[run].Details)) {
		cmd = -1
	}
	template_data.cmd_idx = cmd
}
