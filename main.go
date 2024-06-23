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
const jobsDir = "./examples/"
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
	Name              string
	Cmd               string
	RenderedCmd       string
	StartTime         time.Time
	EndTime           time.Time
	Status            RunStatus
	Attempt           int
	CmdTemplateParams map[string]string
	//todo: store in db?
	LastOutput string
}

type JobRun struct {
	ScheduledTime time.Time
	StartTime     time.Time
	EndTime       time.Time
	Status        RunStatus
	TasksHistory  []*TaskRun
}

type Job struct {
	File       string
	MD5        [16]byte
	Title      string  `json:"title"`
	Cron       string  `json:"cron"`
	Tasks      []*Task `json:"tasks"`
	cronID     cron.EntryID
	RunHistory []*JobRun
	OnOff      bool
}

type AllJobs struct {
	Jobs []*Job
}

func main() {
	var jobs AllJobs
	c := cron.New(cron.WithSeconds())
	c.AddFunc(
		scanSchedule,
		func() { scanAndScheduleJobs(&jobs, c) },
	)
	c.Start()
	httpServer(&jobs)
}

func scanAndScheduleJobs(jobs *AllJobs, c *cron.Cron) {
	var todelete []int
	files := make(map[string][16]byte)
	err := scanFiles(files)
	if err != nil {
		slog.Errorf("Errors while reading files")
	}
	for idx, jb := range jobs.Jobs {
		md5, haskey := files[jb.File]
		if !haskey {
			slog.Infof("marking %s for deletion", jb.Title)
			todelete = append(todelete, idx)
		} else if md5 != jb.MD5 {
			slog.Infof("file %s has changed, marking for reloading", jb.File)
			todelete = append(todelete, idx)
		} else if md5 == jb.MD5 {
			slog.Infof("file %s has not changed, skipping", jb.File)
			delete(files, jb.File)
		} else {
			panic("This is not supposed to happen")
		}
	}
	//todo: simplify removing
	if len(todelete) > 0 {
		sort.Sort(sort.Reverse(sort.IntSlice(todelete)))
		last_idx := len(jobs.Jobs) - 1
		for _, jb_idx := range todelete {
			c.Remove(jobs.Jobs[jb_idx].cronID)
			jobs.Jobs[jb_idx] = jobs.Jobs[last_idx]
			last_idx = last_idx - 1
		}
		if last_idx >= 0 {
			jobs.Jobs = jobs.Jobs[:last_idx]
		} else {
			jobs.Jobs = nil
		}
	}
	for f := range files {
		slog.Infof("loading %s", f)
		jb, _ := processJobFile(f)
		//if err != nil { }
		scheduleJob(jb, jobs, c)
	}
}

func scanFiles(files map[string][16]byte) error {
	dir := jobsDir
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		//todo: dont return on error,
		//continue with other files
		if err != nil {
			slog.Errorf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".job" {
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

func processJobFile(filePath string) (*Job, error) {
	var jb Job
	jb.File = filePath
	jb.OnOff = true
	f, err := os.ReadFile(filePath)
	if err != nil {
		slog.Errorf("Error reading file %s: %v\n", filePath, err)
		return nil, err
	}
	jb.MD5 = md5.Sum(f)
	err = json.Unmarshal(f, &jb)
	if err != nil {
		slog.Errorf("Error parsing file %s: %v\n", filePath, err)
		return nil, err
	}
	return &jb, nil
}

func scheduleJob(jb *Job, jobs *AllJobs, c *cron.Cron) {
	jobs.Jobs = append(jobs.Jobs, jb)
	jb.cronID, _ = c.AddFunc(
		jb.Cron,
		func() { runScheduled(jb, c) },
	)
	slog.Infof("Added job '%s' from file '%s'", jb.Title, jb.File)
}

func runScheduled(jb *Job, c *cron.Cron) {
	//todo: don't pass *cron.Cron
	if !jb.OnOff {
		slog.Infof("Skipping '%s'", jb.Title)
		return
	}
	run := initRun(jb, c)
	runJob(run, jb)
	//todo: check for errors
}

func initRun(jb *Job, c *cron.Cron) *JobRun {
	run := &JobRun{
		//todo: get scheduled run time for the job
		ScheduledTime: c.Entry(jb.cronID).Prev,
		StartTime:     time.Now(),
	}
	for _, t := range jb.Tasks {
		run.TasksHistory = append(run.TasksHistory, &TaskRun{
			Name:    t.Name,
			Cmd:     t.Cmd,
			Status:  NoRun,
			Attempt: 0,
			CmdTemplateParams: map[string]string{
				"title":        jb.Title,
				"scheduled_dt": run.ScheduledTime.Format("2006-01-02"),
			}})
	}
	jb.RunHistory = append(jb.RunHistory, run)
	return run
}

func runJob(run *JobRun, jb *Job) error {
	run.Status = Running
	slog.Infof("Running '%s'", jb.Title)
	var jobFail bool
	for _, tr := range run.TasksHistory {
		err := runCommand(tr)
		if err != nil {
			jobFail = true
			break
		}
	}
	run.Status = RunSuccess
	if jobFail {
		run.Status = RunFailure
	}
	run.EndTime = time.Now()
	//todo: return error
	return nil
}

func runCommand(tr *TaskRun) error {
	tmpl := texttemplate.New("tmpl")
	tmpl, err := tmpl.Parse(tr.Cmd)
	if err != nil {
		slog.Errorf("Error parsing command template '%s'-'%s'-'%s': %v\n", tr.CmdTemplateParams["title"], tr.Name, tr.Cmd, err)
		return err
	}
	sb := new(strings.Builder)
	err = tmpl.Execute(sb, tr.CmdTemplateParams)
	if err != nil {
		slog.Errorf("Error rendering command template '%s'-'%s'-'%s': %v\n", tr.CmdTemplateParams["title"], tr.Name, tr.Cmd, err)
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
		slog.Errorf("Error executing '%s'-'%s': %v\n", tr.CmdTemplateParams["title"], tr.Name, err)
		tr.Status = RunFailure
	}
	return err
}

func executeCmd(command string) (string, error) {
	cmd := exec.Command("/bin/bash", "-c", command)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func restartJobRun(jb *Job, run *JobRun) {
	run.Status = NoRun
	for _, tr := range run.TasksHistory {
		tr.Status = NoRun
	}
	runJob(run, jb)
}

func restartTaskRun(taskRun *TaskRun) {
	runCommand(taskRun)
	//todo: add error check
}

func jobOnOff(jobidx int, jobs *AllJobs) error {
	if jobidx >= len(jobs.Jobs) || jobidx < 0 {
		slog.Errorf("incorrect job index %v", jobidx)
		return errors.New("incorrect job index")
	}
	jb := jobs.Jobs[jobidx]
	jb.OnOff = !jb.OnOff
	slog.Infof("Toggled state of %s to %v", jb.Title, jb.OnOff)
	return nil
}

func (jb Job) CountFailed() int {
	//todo: maintain counter?
	f := 0
	for _, h := range jb.RunHistory {
		if h.Status == RunFailure {
			f += 1
		}
	}
	return f
}

const webJobsList = `
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
		h1 a {
			color: black;
			text-decoration: none;
		}
		.job {
			margin-bottom: 20px;
			overflow-x: auto;
			overflow-y: hidden;
		}
		table {
			width: 100%;
		}
		table th {
			font-size: 1.2em;
			font-weight: normal;
			text-align: left;
			margin-bottom: 10px;
		}
		th.l1, td.l1 {
			width: 2rem;
			min-width: 2rem;
			position: sticky;
			left: 0;
			background-color: white;
		}
		th.l2, td.l2 {
			width: 15rem;
			min-width: 15rem;
			position: sticky;
			left: 2rem;
			background-color: white;
		}
		th.st, td.st {
			vertical-align: middle;
			text-align: center;
			width: 1.2rem;
		}
		th.sel, td.sel {
			border-bottom-style: solid;
			border-width: medium;
		}
		th.r1, td.r1 {
			width: 5rem;
			min-width: 5rem;
			text-align: right;
			position: sticky;
			right: 0;
			background-color: white;
		}
		th.r2, td.r2 {
			width: 10rem;
			min-width: 10rem;
			text-align: right;
			position: sticky;
			right: 5rem;
			background-color: white;
		}
		table a {
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
	{{.HTMLListJobs}}
	<script>
		function onoff(job) {
			fetch('/onoff?job=' + job)
				.then(response => {
					location.reload();
				})
				.catch(error => {
					console.error('Error toggling state:', error);
				});
		}
		function restart(job, run, cmd) {
			fetch('/restart?job=' + job + '&run=' + run + '&cmd=' + cmd)
				.then(response => {
					location.reload();
				})
				.catch(error => {
					console.error('Error restarting job:', error);
				});
		}
		function showhide(job) {
			for (const e of document.querySelectorAll('.hist' + job)) {
        		if ( e.style.visibility == 'visible' )
            		e.style.visibility = 'collapse';
        		else
            		e.style.visibility = 'visible';
			}
			var b = document.getElementById('showhidebtn' + job);
			if (b.innerText == '-')
				b.innerText = '+';
			else 
				b.innerText = '-';
		}
		document.addEventListener("DOMContentLoaded", function() {
			for (const e of document.querySelectorAll('.job')) {
				e.scrollLeft = e.scrollWidth;
			}
		});
	</script>
</body>
</html>
`

type HTMLTemplateData struct {
	job_idx int
	run_idx int
	cmd_idx int
	Title   string
	Jobs    *AllJobs
}

func (td HTMLTemplateData) HTMLListJobs() template.HTML {
	var sb strings.Builder
	var btn_text string
	var cron_text string
	var visible bool
	var visibility string
	var err error
	exprDesc, _ := hcron.NewDescriptor(hcron.Use24HourTimeFormat(true))
	sb.WriteString(fmt.Sprintf("<h1><a href=\"/\">%s</a></h1>\n", td.Title))
	for job_idx, jb := range td.Jobs.Jobs {
		sb.WriteString("<div>\n")
		sb.WriteString(fmt.Sprintf("<div class=\"job\" id=\"job%v\">", job_idx))
		sb.WriteString("<table>\n")
		// header
		sb.WriteString("<tr>\n")
		if jb.CountFailed() > 0 || td.job_idx == job_idx {
			visible = true
			btn_text = "-"
		} else {
			visible = false
			btn_text = "+"
		}
		sb.WriteString(fmt.Sprintf("<th class=\"l1\"><button id=\"showhidebtn%v\" onclick=\"showhide(%v)\">%s</button></th>", job_idx, job_idx, btn_text))
		sb.WriteString(fmt.Sprintf("<th class=\"l2\"><strong>%s</strong></th>", jb.Title))
		for c := 0; c < len(jb.RunHistory); c++ {
			if td.job_idx == job_idx && td.run_idx == c && td.cmd_idx == -1 {
				sb.WriteString("<th class=\"st sel\">")
			} else {
				sb.WriteString("<th class=\"st\">")
			}
			sb.WriteString(fmt.Sprintf("<a href=\"/?job=%v&run=%v#job%v\">%s</a>", job_idx, c, job_idx, jb.RunHistory[c].Status.HTMLStatus()))
			sb.WriteString("</th>")
		}
		sb.WriteString("<th class=\"st\">&#9633;</th>")
		sb.WriteString("<th class=\"fill\"> </th>")
		cron_text, err = exprDesc.ToDescription(jb.Cron, hcron.Locale_en)
		if err != nil {
			cron_text = jb.Cron
		}
		sb.WriteString(fmt.Sprintf("<th class=\"r2\">%s</th>", cron_text))
		if jb.OnOff {
			btn_text = "Turn Off"
		} else {
			btn_text = "Turn On"
		}
		sb.WriteString(fmt.Sprintf("<th class=\"r1\"><button onclick=\"onoff( %v )\">%s</button></th>\n", job_idx, btn_text))
		sb.WriteString("</tr>\n")
		// tasks statuses
		for r := 0; r < len(jb.Tasks); r++ {
			if visible {
				visibility = "style=\"visibility: visible;\""
			} else {
				visibility = "style=\"visibility: collapse;\""
			}
			sb.WriteString(fmt.Sprintf("<tr class=\"hist%v\" %s>\n", job_idx, visibility))
			for c := -1; c <= len(jb.RunHistory); c++ {
				if c == -1 {
					sb.WriteString("<td class=\"l1\"> </td>")
					sb.WriteString(fmt.Sprintf("<td class=\"l2\"> %s </td>", html.EscapeString(jb.Tasks[r].Name)))
				} else if c < len(jb.RunHistory) {
					if td.job_idx == job_idx && td.run_idx == c && td.cmd_idx == r {
						sb.WriteString("<td class=\"st sel\">")
					} else {
						sb.WriteString("<td class=\"st\">")
					}
					sb.WriteString(fmt.Sprintf("<a href=\"/?job=%v&run=%v&cmd=%v#job%v\">%s</a>", job_idx, c, r, job_idx, jb.RunHistory[c].TasksHistory[r].Status.HTMLStatus()))
					sb.WriteString("</td>")
				} else if c == len(jb.RunHistory) {
					sb.WriteString("<td class=\"st\">&#9633;</td>")
					sb.WriteString("<td class=\"fill\"> </td>")
					sb.WriteString("<td class=\"r2\"> </td>")
					sb.WriteString("<td class=\"r1\"> </td>\n")
				} else {
					slog.Error("this is not supposed to happen")
				}
			}
			sb.WriteString("</tr>\n")
		}
		sb.WriteString("</table>\n")
		sb.WriteString("</div>\n")
		if td.job_idx == job_idx {
			if td.run_idx != -1 {
				run := jb.RunHistory[td.run_idx]
				sb.WriteString("<p>")
				sb.WriteString(fmt.Sprintf("%s: ", run.ScheduledTime.Format("02 Jan 06 15:04")))
				sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart all</button> ", td.job_idx, td.run_idx, td.cmd_idx))
				//sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart failed & dependencies</button>", td.job_idx, td.run_idx, td.cmd_idx))
				sb.WriteString("</p>")
			}
			if td.run_idx != -1 && td.cmd_idx != -1 {
				cmd_run := jb.RunHistory[td.run_idx].TasksHistory[td.cmd_idx]
				sb.WriteString("<p>")
				sb.WriteString(fmt.Sprintf("%s: ", cmd_run.Name))
				sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart task</button> ", td.job_idx, td.run_idx, td.cmd_idx))
				//todo: define restart_with_dependencies
				//sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart task & dependencies</button>", td.job_idx, td.run_idx, td.cmd_idx))
				sb.WriteString("</p>")
				sb.WriteString("<pre>")
				sb.WriteString(fmt.Sprintf("<code>> %s </code>\n", cmd_run.RenderedCmd))
				sb.WriteString(fmt.Sprintf("<samp>%s</samp>", cmd_run.LastOutput))
				sb.WriteString("</pre>")
			}
		}
		sb.WriteString("</div>\n")
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

func (td HTMLTemplateData) HTMLHistoryTable(job_idx int) string {
	jb := td.Jobs.Jobs[job_idx]
	var sb strings.Builder
	sb.WriteString("<table>\n")
	for r := -1; r < len(jb.Tasks); r++ {
		sb.WriteString("<tr>\n")
		for c := -1; c <= len(jb.RunHistory); c++ {
			if r == -1 && c == -1 {
				sb.WriteString("<th> </th>")
			} else if r == -1 && c < len(jb.RunHistory) {
				sb.WriteString(fmt.Sprintf("<th> <a href=\"/?job=%v&run=%v\">%s</a> </th>", job_idx, c, jb.RunHistory[c].Status.HTMLStatus()))
			} else if r == -1 && c == len(jb.RunHistory) {
				sb.WriteString("<th>&#9633;</th>")
			} else if c == -1 {
				sb.WriteString(fmt.Sprintf("<td> %s </td>", html.EscapeString(jb.Tasks[r].Name)))
			} else if c < len(jb.RunHistory) {
				sb.WriteString(fmt.Sprintf("<td> <a href=\"/?job=%v&run=%v&cmd=%v\">%s</a> </td>", job_idx, c, r, jb.RunHistory[c].TasksHistory[r].Status.HTMLStatus()))
			} else if c == len(jb.RunHistory) {
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

func httpServer(jobs *AllJobs) {
	//todo: init once
	template_data := &HTMLTemplateData{Jobs: jobs, Title: htmlTitle}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpListJobs(w, r, template_data)
	})
	http.HandleFunc("/onoff", func(w http.ResponseWriter, r *http.Request) {
		httpOnOff(w, r, template_data)
	})
	http.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		httpRestart(w, r, template_data)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func httpListJobs(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	httpParseJobRunCmd(r, template_data)
	tmpl := template.New("tmpl")
	tmpl = template.Must(tmpl.Parse(webJobsList))
	err := tmpl.Execute(w, template_data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func httpOnOff(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	httpParseJobRunCmd(r, template_data)
	err := jobOnOff(template_data.job_idx, template_data.Jobs)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
	}
}

func httpRestart(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	httpParseJobRunCmd(r, template_data)
	var jb *Job
	jb = nil
	if template_data.job_idx != -1 {
		jb = template_data.Jobs.Jobs[template_data.job_idx]
	}
	var rn *JobRun
	rn = nil
	if jb != nil && template_data.run_idx != -1 {
		rn = jb.RunHistory[template_data.run_idx]
	}
	var c *TaskRun
	c = nil
	if rn != nil && template_data.cmd_idx != -1 {
		c = rn.TasksHistory[template_data.cmd_idx]
	}
	if rn != nil && c != nil {
		restartTaskRun(c)
	} else if rn != nil {
		restartJobRun(jb, rn)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
	}
}

func httpParseJobRunCmd(r *http.Request, template_data *HTMLTemplateData) {
	job_str := r.URL.Query().Get("job")
	jb, err := strconv.Atoi(job_str)
	if err != nil {
		jb = -1
	} else if jb < 0 || jb >= len(template_data.Jobs.Jobs) {
		jb = -1
	}
	template_data.job_idx = jb
	run_str := r.URL.Query().Get("run")
	run, err := strconv.Atoi(run_str)
	if err != nil {
		run = -1
	} else if jb != -1 && (run < 0 || run >= len(template_data.Jobs.Jobs[jb].RunHistory)) {
		run = -1
	}
	template_data.run_idx = run
	cmd_str := r.URL.Query().Get("cmd")
	cmd, err := strconv.Atoi(cmd_str)
	if err != nil {
		cmd = -1
	} else if jb != -1 && run != -1 && (cmd < 0 || cmd >= len(template_data.Jobs.Jobs[jb].RunHistory[run].TasksHistory)) {
		cmd = -1
	}
	template_data.cmd_idx = cmd
}
