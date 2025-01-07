package main

import (
	"crypto/md5"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gookit/slog"
	"github.com/robfig/cron/v3"
)

const port = ":8080"
const jobsDir = "./examples/"
const scanSchedule = "*/10 * * * * *"

//go:embed index.html
var embedded embed.FS

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
	Running
	NoRun
)

type Task struct {
	Name string `toml:"name"`
	Cmd  string `toml:"cmd"`
}

type TaskRun struct {
	Name              string
	cmd               string
	renderedCmd       string
	StartTime         time.Time
	EndTime           time.Time
	Status            RunStatus
	Attempt           int
	cmdTemplateParams map[string]string
	//todo: store in db
	lastOutput string
}

type JobRun struct {
	ScheduledTime time.Time
	StartTime     time.Time
	EndTime       time.Time
	Status        RunStatus
	TasksHistory  []*TaskRun
}

type Job struct {
	file       string
	md5        [16]byte
	Title      string  `toml:"title" json:"Title"`
	Cron       string  `toml:"cron" json:"Cron"`
	Tasks      []*Task `toml:"tasks" json:"Tasks"`
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
	files := make(map[string][16]byte)
	err := scanFiles(files)
	if err != nil {
		slog.Errorf("Errors while reading files")
	}
	removeJobsWithoutFiles(files, jobs, c)
	for f := range files {
		slog.Infof("Loading %s", f)
		jb, err := processJobFile(f)
		if jb != nil && err == nil {
			scheduleJob(jb, jobs, c)
		} else {
			slog.Infof("Skipping %s", f)
		}
	}
	sort.SliceStable(jobs.Jobs, func(i, j int) bool {
		return jobs.Jobs[i].Title < jobs.Jobs[j].Title
	})
}

func scanFiles(files map[string][16]byte) error {
	dir := jobsDir
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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

func removeJobsWithoutFiles(files map[string][16]byte, jobs *AllJobs, c *cron.Cron) {
	//todo: simplify removing
	var toremove []int
	for idx, jb := range jobs.Jobs {
		md5, haskey := files[jb.file]
		if !haskey {
			slog.Infof("Marking %s for deletion", jb.Title)
			toremove = append(toremove, idx)
		} else if md5 != jb.md5 {
			slog.Infof("File %s has changed, marking for reloading", jb.file)
			toremove = append(toremove, idx)
		} else if md5 == jb.md5 {
			slog.Infof("File %s has not changed, skipping", jb.file)
			delete(files, jb.file)
		} else {
			panic("This is not supposed to happen")
		}
	}
	if len(toremove) > 0 {
		sort.Sort(sort.Reverse(sort.IntSlice(toremove)))
		last_idx := len(jobs.Jobs) - 1
		for _, jb_idx := range toremove {
			c.Remove(jobs.Jobs[jb_idx].cronID)
			jobs.Jobs[jb_idx] = jobs.Jobs[last_idx]
			last_idx = last_idx - 1
		}
		if last_idx >= 0 {
			jobs.Jobs = jobs.Jobs[:last_idx+1]
		} else {
			jobs.Jobs = nil
		}
	}
}

func processJobFile(filePath string) (*Job, error) {
	var jb Job
	jb.file = filePath
	jb.OnOff = false
	f, err := os.ReadFile(filePath)
	if err != nil {
		slog.Errorf("Error reading file %s: %v\n", filePath, err)
		return nil, err
	}
	jb.md5 = md5.Sum(f)
	jb.RunHistory = make([]*JobRun, 0)
	err = toml.Unmarshal(f, &jb)
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
	slog.Infof("Added job '%s' from file '%s'", jb.Title, jb.file)
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
	//todo: don't pass *cron.Cron
	var scheduled_time time.Time
	if c != nil {
		scheduled_time = c.Entry(jb.cronID).Prev
	} else {
		scheduled_time = time.Now()
	}
	run := &JobRun{
		//todo: get scheduled run time for the job
		ScheduledTime: scheduled_time,
		StartTime:     time.Now(),
	}
	for _, t := range jb.Tasks {
		run.TasksHistory = append(run.TasksHistory, &TaskRun{
			Name:    t.Name,
			cmd:     t.Cmd,
			Status:  NoRun,
			Attempt: 0,
			cmdTemplateParams: map[string]string{
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
	tmpl, err := tmpl.Parse(tr.cmd)
	if err != nil {
		slog.Errorf("Error parsing command template '%s'-'%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, tr.cmd, err)
		return err
	}
	sb := new(strings.Builder)
	err = tmpl.Execute(sb, tr.cmdTemplateParams)
	if err != nil {
		slog.Errorf("Error rendering command template '%s'-'%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, tr.cmd, err)
		return err
	}
	tr.StartTime = time.Now()
	tr.Attempt += tr.Attempt
	tr.renderedCmd = sb.String()
	tr.Status = Running
	output, err := executeCmd(tr.renderedCmd)
	tr.lastOutput = output
	tr.EndTime = time.Now()
	tr.Status = RunSuccess
	if err != nil {
		slog.Errorf("Error executing '%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, err)
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

func runNow(jb *Job) error {
	run := initRun(jb, nil)
	err := runJob(run, jb)
	return err
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

// todo: switch to client-side rendering
// func (td HTMLTemplateData) HTMLListJobs() template.HTML {
// 	var sb strings.Builder
// 	var btn_text string
// 	var cron_text string
// 	var visible bool
// 	var visibility string
// 	var err error
// 	exprDesc, _ := hcron.NewDescriptor(hcron.Use24HourTimeFormat(true))
// 	sb.WriteString(fmt.Sprintf("<h1><a href=\"/\">%s</a></h1>\n", td.Title))
// 	for job_idx, jb := range td.Jobs.Jobs {
// 		sb.WriteString("<div>\n")
// 		sb.WriteString(fmt.Sprintf("<div class=\"job\" id=\"job%v\">", job_idx))
// 		sb.WriteString("<table>\n")
// 		// header
// 		sb.WriteString("<tr>\n")
// 		if jb.CountFailed() > 0 || td.job_idx == job_idx {
// 			visible = true
// 			btn_text = "-"
// 		} else {
// 			visible = false
// 			btn_text = "+"
// 		}
// 		sb.WriteString(fmt.Sprintf("<th class=\"collapse_btn\"><button id=\"showhidebtn%v\" onclick=\"showhide(%v)\">%s</button></th>", job_idx, job_idx, btn_text))
// 		sb.WriteString(fmt.Sprintf("<th class=\"task_names\"><strong>%s</strong></th>", jb.Title))
// 		for c := 0; c < len(jb.RunHistory); c++ {
// 			if td.job_idx == job_idx && td.run_idx == c && td.cmd_idx == -1 {
// 				sb.WriteString("<th class=\"states selected\">")
// 			} else {
// 				sb.WriteString("<th class=\"states\">")
// 			}
// 			sb.WriteString(fmt.Sprintf("<a href=\"/?job=%v&run=%v#job%v\">%s</a>", job_idx, c, job_idx, jb.RunHistory[c].Status.HTMLStatus()))
// 			sb.WriteString("</th>")
// 		}
// 		sb.WriteString("<th class=\"states\">&#9633;</th>")
// 		sb.WriteString("<th class=\"fill\"> </th>")
// 		cron_text, err = exprDesc.ToDescription(jb.Cron, hcron.Locale_en)
// 		if err != nil {
// 			cron_text = jb.Cron
// 		}
// 		sb.WriteString(fmt.Sprintf("<th class=\"schedule\">%s</th>", cron_text))
// 		sb.WriteString(fmt.Sprintf("<th class=\"runnow_btn\"><button onclick=\"runnow( %v )\">Run Now</button></th>\n", job_idx))
// 		if jb.OnOff {
// 			btn_text = "Turn Off"
// 		} else {
// 			btn_text = "Turn On"
// 		}
// 		sb.WriteString(fmt.Sprintf("<th class=\"onoff_btn\"><button onclick=\"onoff( %v )\">%s</button></th>\n", job_idx, btn_text))
// 		sb.WriteString("</tr>\n")
// 		// tasks statuses
// 		for r := 0; r < len(jb.Tasks); r++ {
// 			if visible {
// 				visibility = "style=\"visibility: visible;\""
// 			} else {
// 				visibility = "style=\"visibility: collapse;\""
// 			}
// 			sb.WriteString(fmt.Sprintf("<tr class=\"hist%v\" %s>\n", job_idx, visibility))
// 			for c := -1; c <= len(jb.RunHistory); c++ {
// 				if c == -1 {
// 					sb.WriteString("<td class=\"collapse_btn\"> </td>")
// 					sb.WriteString(fmt.Sprintf("<td class=\"task_names\"> %s </td>", html.EscapeString(jb.Tasks[r].Name)))
// 				} else if c < len(jb.RunHistory) {
// 					if td.job_idx == job_idx && td.run_idx == c && td.cmd_idx == r {
// 						sb.WriteString("<td class=\"states selected\">")
// 					} else {
// 						sb.WriteString("<td class=\"states\">")
// 					}
// 					sb.WriteString(fmt.Sprintf("<a href=\"/?job=%v&run=%v&cmd=%v#job%v\">%s</a>", job_idx, c, r, job_idx, jb.RunHistory[c].TasksHistory[r].Status.HTMLStatus()))
// 					sb.WriteString("</td>")
// 				} else if c == len(jb.RunHistory) {
// 					sb.WriteString("<td class=\"states\">&#9633;</td>")
// 					sb.WriteString("<td class=\"fill\"> </td>")
// 					sb.WriteString("<td class=\"schedule\"> </td>")
// 					sb.WriteString("<td class=\"runnow_btn\"> </td>")
// 					sb.WriteString("<td class=\"onoff_btn\"> </td>\n")
// 				} else {
// 					slog.Error("this is not supposed to happen")
// 				}
// 			}
// 			sb.WriteString("</tr>\n")
// 		}
// 		sb.WriteString("</table>\n")
// 		sb.WriteString("</div>\n")
// 		if td.job_idx == job_idx {
// 			if td.run_idx != -1 {
// 				run := jb.RunHistory[td.run_idx]
// 				sb.WriteString("<p>")
// 				sb.WriteString(fmt.Sprintf("%s: ", run.ScheduledTime.Format("02 Jan 06 15:04:05")))
// 				sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart job</button> ", td.job_idx, td.run_idx, td.cmd_idx))
// 				//sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart failed & dependencies</button>", td.job_idx, td.run_idx, td.cmd_idx))
// 				sb.WriteString("</p>")
// 			}
// 			if td.run_idx != -1 && td.cmd_idx != -1 {
// 				cmd_run := jb.RunHistory[td.run_idx].TasksHistory[td.cmd_idx]
// 				sb.WriteString("<p>")
// 				sb.WriteString(fmt.Sprintf("%s: ", cmd_run.Name))
// 				sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart task</button> ", td.job_idx, td.run_idx, td.cmd_idx))
// 				//todo: define restart_with_dependencies
// 				//sb.WriteString(fmt.Sprintf("<button onclick=\"restart( %v, %v, %v )\">Restart task & dependencies</button>", td.job_idx, td.run_idx, td.cmd_idx))
// 				sb.WriteString("</p>")
// 				sb.WriteString("<pre>")
// 				sb.WriteString(fmt.Sprintf("<code>> %s </code>\n", cmd_run.RenderedCmd))
// 				sb.WriteString(fmt.Sprintf("<samp>%s</samp>", cmd_run.lastOutput))
// 				sb.WriteString("</pre>")
// 			}
// 		}
// 		sb.WriteString("</div>\n")
// 	}
// 	return template.HTML(sb.String())
// }

// func (td HTMLTemplateData) HTMLHistoryTable(job_idx int) string {
// 	jb := td.Jobs.Jobs[job_idx]
// 	var sb strings.Builder
// 	sb.WriteString("<table>\n")
// 	for r := -1; r < len(jb.Tasks); r++ {
// 		sb.WriteString("<tr>\n")
// 		for c := -1; c <= len(jb.RunHistory); c++ {
// 			if r == -1 && c == -1 {
// 				sb.WriteString("<th> </th>")
// 			} else if r == -1 && c < len(jb.RunHistory) {
// 				sb.WriteString(fmt.Sprintf("<th> <a href=\"/?job=%v&run=%v\">%s</a> </th>", job_idx, c, jb.RunHistory[c].Status.HTMLStatus()))
// 			} else if r == -1 && c == len(jb.RunHistory) {
// 				sb.WriteString("<th>&#9633;</th>")
// 			} else if c == -1 {
// 				sb.WriteString(fmt.Sprintf("<td> %s </td>", html.EscapeString(jb.Tasks[r].Name)))
// 			} else if c < len(jb.RunHistory) {
// 				sb.WriteString(fmt.Sprintf("<td> <a href=\"/?job=%v&run=%v&cmd=%v\">%s</a> </td>", job_idx, c, r, jb.RunHistory[c].TasksHistory[r].Status.HTMLStatus()))
// 			} else if c == len(jb.RunHistory) {
// 				sb.WriteString("<td>&#9633;</td>")
// 			} else {
// 				slog.Error("this is not supposed to happen")
// 			}
// 		}
// 		sb.WriteString("</tr>\n")
// 	}
// 	sb.WriteString("</table>\n")
// 	return sb.String()
// }

type HTMLTemplateData struct {
	job_idx int
	run_idx int
	cmd_idx int
	Jobs    *AllJobs
}

func httpServer(jobs *AllJobs) {
	template_data := &HTMLTemplateData{Jobs: jobs}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpIndex(w, r, template_data)
	})
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		httpJobs(w, r, template_data)
	})
	http.HandleFunc("/onoff", func(w http.ResponseWriter, r *http.Request) {
		httpOnOff(w, r, template_data)
	})
	http.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		httpRestart(w, r, template_data)
	})
	http.HandleFunc("/runnow", func(w http.ResponseWriter, r *http.Request) {
		httpRunNow(w, r, template_data)
	})
	slog.Fatal(http.ListenAndServe(port, nil))
}

func httpIndex(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	data, err := embedded.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Error loading the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(data)
}

func httpJobs(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	// todo: control fields
	jData, err := json.Marshal(template_data.Jobs)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	//slog.Infof(string(jData))
	w.Header().Set("Content-Type", "application/json")
	w.Write(jData)
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

func httpRunNow(w http.ResponseWriter, r *http.Request, template_data *HTMLTemplateData) {
	httpParseJobRunCmd(r, template_data)
	var jb *Job
	jb = nil
	if template_data.job_idx != -1 {
		jb = template_data.Jobs.Jobs[template_data.job_idx]
	}
	err := runNow(jb)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
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
