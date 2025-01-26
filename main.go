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
	hcron "github.com/lnquy/cron"
	"github.com/robfig/cron/v3"
)

//go:embed index.html
var embedded embed.FS

var CONF Config

type Config struct {
	port    string
	jobsDir string
}

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
	RenderedCmd       string
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
	file          string
	md5           [16]byte
	Title         string `toml:"title"`
	Cron          string `toml:"cron"`
	HCron         string
	Tasks         []*Task `toml:"tasks"`
	cronID        cron.EntryID
	RunHistory    []*JobRun
	OnOff         bool
	NextScheduled time.Time
}

type JobsAndCron struct {
	Jobs []*Job
	cron *cron.Cron
	//todo: add config, make global?
}

func main() {
	var JC JobsAndCron
	const scanSchedule = "*/10 * * * * *"
	initConfig()
	JC.cron = cron.New(cron.WithSeconds())
	JC.cron.AddFunc(
		scanSchedule,
		func() { scanAndScheduleJobs(&JC) },
	)
	JC.cron.Start()
	httpServer(&JC)
}

func initConfig() {
	CONF.port = ":8080"
	CONF.jobsDir = "./examples/"
	if port := os.Getenv("REPEATER_PORT"); port != "" {
		CONF.port = port
	}
	if jobsDir := os.Getenv("REPEATER_JOBS_DIRECTORY"); jobsDir != "" {
		CONF.jobsDir = jobsDir
	}
}

func scanAndScheduleJobs(JC *JobsAndCron) {
	files := make(map[string][16]byte)
	err := scanFiles(files)
	if err != nil {
		slog.Errorf("Errors while reading files")
	}
	removeJobsWithoutFiles(files, JC)
	for f := range files {
		slog.Infof("Loading %s", f)
		jb, err := processJobFile(f)
		if jb != nil && err == nil {
			scheduleJob(jb, JC)
		} else {
			slog.Infof("Skipping %s", f)
		}
	}
	sort.SliceStable(JC.Jobs, func(i, j int) bool {
		return JC.Jobs[i].Title < JC.Jobs[j].Title
	})
}

func scanFiles(files map[string][16]byte) error {
	err := filepath.Walk(CONF.jobsDir, func(path string, info os.FileInfo, err error) error {
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

func removeJobsWithoutFiles(files map[string][16]byte, JC *JobsAndCron) {
	//todo: simplify removing
	var toremove []int
	for idx, jb := range JC.Jobs {
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
		last_idx := len(JC.Jobs) - 1
		for _, jb_idx := range toremove {
			JC.cron.Remove(JC.Jobs[jb_idx].cronID)
			JC.Jobs[jb_idx] = JC.Jobs[last_idx]
			last_idx = last_idx - 1
		}
		if last_idx >= 0 {
			JC.Jobs = JC.Jobs[:last_idx+1]
		} else {
			JC.Jobs = nil
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
	exprDesc, _ := hcron.NewDescriptor(hcron.Use24HourTimeFormat(true))
	jb.HCron, err = exprDesc.ToDescription(jb.Cron, hcron.Locale_en)
	if err != nil {
		jb.HCron = jb.Cron
	}
	return &jb, nil
}

func scheduleJob(jb *Job, JC *JobsAndCron) {
	JC.Jobs = append(JC.Jobs, jb)
	jb.cronID, _ = JC.cron.AddFunc(
		jb.Cron,
		func() { runScheduled(jb, JC.cron) },
	)
	slog.Infof("Added job '%s' from file '%s'", jb.Title, jb.file)
}

func runScheduled(jb *Job, c *cron.Cron) {
	if !jb.OnOff {
		slog.Infof("Skipping '%s'", jb.Title)
		return
	}
	run := initRun(jb, c)
	runJob(run, jb)
	jb.NextScheduled = c.Entry(jb.cronID).Next
	//todo: check for errors
}

func initRun(jb *Job, c *cron.Cron) *JobRun {
	var scheduled_time time.Time
	if c != nil {
		scheduled_time = c.Entry(jb.cronID).Prev
	} else {
		scheduled_time = time.Now()
	}
	run := &JobRun{
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
		err := runTask(tr)
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

func runTask(tr *TaskRun) error {
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
	tr.RenderedCmd = sb.String()
	tr.Status = Running
	output, err := executeCmd(tr.RenderedCmd)
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
	run.StartTime = time.Time{}
	run.EndTime = time.Time{}
	for _, tr := range run.TasksHistory {
		tr.Status = NoRun
		tr.StartTime = time.Time{}
		tr.EndTime = time.Time{}
		tr.RenderedCmd = ""
		tr.lastOutput = ""
	}
	runJob(run, jb)
}

func restartTaskRun(taskRun *TaskRun) {
	runTask(taskRun)
	//todo: add error check
}

func jobOnOff(jobidx int, JC *JobsAndCron) error {
	if jobidx >= len(JC.Jobs) || jobidx < 0 {
		slog.Errorf("incorrect job index %v", jobidx)
		return errors.New("incorrect job index")
	}
	jb := JC.Jobs[jobidx]
	jb.OnOff = !jb.OnOff
	if jb.OnOff {
		jb.NextScheduled = JC.cron.Entry(jb.cronID).Next
	} else {
		jb.NextScheduled = time.Time{}
	}
	slog.Infof("Toggled state of %s to %v", jb.Title, jb.OnOff)
	return nil
}

func runNow(jb *Job) error {
	run := initRun(jb, nil)
	err := runJob(run, jb)
	return err
}

type HTTPQueryParams struct {
	jobIndex  int
	runIndex  int
	taskIndex int
}

func httpServer(JC *JobsAndCron) {
	httpQPars := new(HTTPQueryParams)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpIndex(w, r)
	})
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		httpJobs(w, r, httpQPars, JC)
	})
	http.HandleFunc("/onoff", func(w http.ResponseWriter, r *http.Request) {
		httpOnOff(w, r, httpQPars, JC)
	})
	http.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		httpRestart(w, r, httpQPars, JC)
	})
	http.HandleFunc("/runnow", func(w http.ResponseWriter, r *http.Request) {
		httpRunNow(w, r, httpQPars, JC)
	})
	http.HandleFunc("/lastoutput", func(w http.ResponseWriter, r *http.Request) {
		httpLastOutput(w, r, httpQPars, JC)
	})
	slog.Fatal(http.ListenAndServe(CONF.port, nil))
}

func httpIndex(w http.ResponseWriter, r *http.Request) {
	data, err := embedded.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Error loading the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(data)
}

func httpJobs(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	jData, err := json.Marshal(JC)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jData)
}

func httpOnOff(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	httpParseJobRunTask(r, httpQPars, JC)
	err := jobOnOff(httpQPars.jobIndex, JC)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
	}
	// todo: w.Write(json.Marshal(JC))
}

func httpRestart(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	var rn *JobRun
	rn = nil
	if jb != nil && httpQPars.runIndex != -1 {
		rn = jb.RunHistory[httpQPars.runIndex]
	}
	var t *TaskRun
	t = nil
	if rn != nil && httpQPars.taskIndex != -1 {
		t = rn.TasksHistory[httpQPars.taskIndex]
	}
	if rn != nil && t != nil {
		restartTaskRun(t)
	} else if rn != nil {
		restartJobRun(jb, rn)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
	}
	// todo: w.Write(json.Marshal(JC))
}

func httpRunNow(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	err := runNow(jb)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
	}
	// todo: w.Write(json.Marshal(JC))
}

func httpLastOutput(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	var rn *JobRun
	rn = nil
	if jb != nil && httpQPars.runIndex != -1 {
		rn = jb.RunHistory[httpQPars.runIndex]
	}
	var t *TaskRun
	t = nil
	if rn != nil && httpQPars.taskIndex != -1 {
		t = rn.TasksHistory[httpQPars.taskIndex]
	}
	if t != nil {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(t.lastOutput))
	} else {
		http.Error(w, "Task not found", http.StatusNotFound)
	}
}

func httpParseJobRunTask(r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	job_str := r.URL.Query().Get("job")
	jb, err := strconv.Atoi(job_str)
	if err != nil {
		jb = -1
	} else if jb < 0 || jb >= len(JC.Jobs) {
		jb = -1
	}
	httpQPars.jobIndex = jb
	run_str := r.URL.Query().Get("run")
	run, err := strconv.Atoi(run_str)
	if err != nil {
		run = -1
	} else if jb != -1 && (run < 0 || run >= len(JC.Jobs[jb].RunHistory)) {
		run = -1
	}
	httpQPars.runIndex = run
	task_str := r.URL.Query().Get("task")
	task, err := strconv.Atoi(task_str)
	if err != nil {
		task = -1
	} else if jb != -1 && run != -1 && (task < 0 || task >= len(JC.Jobs[jb].RunHistory[run].TasksHistory)) {
		task = -1
	}
	httpQPars.taskIndex = task
}
