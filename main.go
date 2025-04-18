package main

import (
	"crypto/md5"
	"crypto/rand"
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
	"log" //todo: use log/slog

	"github.com/BurntSushi/toml"
	hcron "github.com/lnquy/cron"
	"github.com/robfig/cron/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/fsnotify/fsnotify"
)

//go:embed index.html
var embedded embed.FS

var jwtSecretKey []byte

var infoLog *log.Logger
var errorLog *log.Logger
var webLog *log.Logger
var webLogBuf strings.Builder

var CONF Config

type Config struct {
	port     string
	jobsDir  string
	password string
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
	//todo: add mutex?
	//todo: add config, make global?
}

func main() {
	var JC JobsAndCron
	initConfig()
	jwtSecretKey = generateRandomKey(32)
	infoLog = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	errorLog = log.New(os.Stdout, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
	webLog = log.New(&webLogBuf, "", log.Ldate|log.Ltime)
	JC.cron = cron.New(cron.WithSeconds())
	JC.cron.Start()
	go startFSWatcher(&JC)
	scanAndScheduleJobs(&JC)
	httpServer(&JC)
}

func initConfig() {
	CONF.port = ":8080"
	CONF.jobsDir = "./examples/"
	CONF.password = ""
	if port := os.Getenv("REPEATER_PORT"); port != "" {
		CONF.port = port
	}
	if jobsDir := os.Getenv("REPEATER_JOBS_DIRECTORY"); jobsDir != "" {
		CONF.jobsDir = jobsDir
	}
	CONF.password = os.Getenv("REPEATER_PASSWORD")
}

func generateRandomKey(size int) []byte {
	key := make([]byte, size)
	_, err := rand.Read(key)
	if err != nil {
		errorLog.Printf("Failed to generate a JWT secret key. Aborting.")
		os.Exit(1)
	}
	return key
}

func startFSWatcher(JC *JobsAndCron) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()
	go watchFS(JC, watcher)
	err = watcher.Add(CONF.jobsDir)
	if err != nil {
		log.Fatal(err)
	}
	select {}
}

func watchFS(JC *JobsAndCron, watcher *fsnotify.Watcher) {
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				return
			}
			//todo: dont do full rescan on each event
			scanAndScheduleJobs(JC)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			errorLog.Println("fsnotify:", err)
		}
	}
}


func scanAndScheduleJobs(JC *JobsAndCron) {
	//todo: add mutex?
	files := make(map[string][16]byte)
	err := scanFiles(files)
	webLogBuf.Reset()
	if err != nil {
		errorLog.Printf("Errors while reading files: %s", err)
		webLog.Printf("Errors while reading files: %s", err)
	}
	removeJobsWithoutFiles(files, JC)
	for f := range files {
		infoLog.Printf("Loading %s", f)
		jb, err := processJobFile(f)
		if jb != nil && err == nil {
			scheduleJob(jb, JC)
		} else {
			infoLog.Printf("Skipping %s", f)
		}
	}
	sort.SliceStable(JC.Jobs, func(i, j int) bool {
		return JC.Jobs[i].Title < JC.Jobs[j].Title
	})
}

func scanFiles(files map[string][16]byte) error {
	err := filepath.Walk(CONF.jobsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			errorLog.Printf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".job" {
			f, err := os.ReadFile(path)
			if err != nil {
				errorLog.Printf("Error reading file %s: %v\n", path, err)
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
	//todo: terminate running commands before removing
	var toremove []int
	for idx, jb := range JC.Jobs {
		md5, haskey := files[jb.file]
		if !haskey {
			infoLog.Printf("Marking %s for deletion", jb.Title)
			toremove = append(toremove, idx)
		} else if md5 != jb.md5 {
			infoLog.Printf("File %s has changed, marking for reloading", jb.file)
			toremove = append(toremove, idx)
		} else if md5 == jb.md5 {
			infoLog.Printf("File %s has not changed, skipping", jb.file)
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
		errorLog.Printf("Error reading file %s: %v\n", filePath, err)
		webLog.Printf("Error reading file %s: %v\n", filePath, err)
		return nil, err
	}
	jb.md5 = md5.Sum(f)
	jb.RunHistory = make([]*JobRun, 0)
	err = toml.Unmarshal(f, &jb)
	if err != nil {
		errorLog.Printf("Error parsing file %s: %v\n", filePath, err)
		webLog.Printf("Error parsing file %s: %v\n", filePath, err)
		return nil, err
	}
	if jb.Title == "" {
		errorLog.Printf("%s: missing job title. Skipping.\n", filePath)
		webLog.Printf("%s: missing job title. Skipping. \n", filePath)
		return nil, nil
	}
	if len(jb.Tasks) == 0 {
		errorLog.Printf("%s: no tasks. Skipping.\n", filePath)
		webLog.Printf("%s: no tasks. Skipping. \n", filePath)
		return nil, nil
	}
	for _, t := range jb.Tasks {
		if len(t.Name) == 0 || len(t.Cmd) == 0 {
			errorLog.Printf("%s: Task name or cmd is empty. Skipping job altogether.\n", filePath)
			webLog.Printf("%s: Task name or cmd is empty. Skipping job altogether. \n", filePath)
			return nil, nil
		}
	}
	//todo: move parser spec into JobsAndCron
	specParser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err = specParser.Parse(jb.Cron)
	if jb.Cron != "" && err != nil {
		errorLog.Printf("%s: can't parse cron \"%s\". %v.\n", filePath, jb.Cron, err)
		webLog.Printf("%s: can't parse cron \"%s\". %v.\n", filePath, jb.Cron, err)
		return nil, err
	}
	exprDesc, _ := hcron.NewDescriptor(hcron.Use24HourTimeFormat(true))
	jb.HCron, err = exprDesc.ToDescription(jb.Cron, hcron.Locale_en)
	if jb.Cron == "" {
		jb.HCron = ""
	} else if err != nil {
		jb.HCron = jb.Cron
	}
	return &jb, nil
}

func scheduleJob(jb *Job, JC *JobsAndCron) {
	var err error
	JC.Jobs = append(JC.Jobs, jb)
	if jb.Cron != "" {
		jb.cronID, err = JC.cron.AddFunc(
			jb.Cron,
			func() { runScheduled(jb, JC.cron) },
		)
		if err != nil {
			errorLog.Printf("Error scheduling job '%s' from file '%s': %v\n", jb.Title, jb.file, err)
			webLog.Printf("Error scheduling job '%s' from file '%s': %v\n", jb.Title, jb.file, err)
			// the error is printed only on the first file read
			// unless the file has changed, the error wont'be printed on consecutive jobs rescan
			// the error is displayed in web only briefly, since weblog is cleared on each rescan
		}
		infoLog.Printf("Added job '%s' from file '%s'", jb.Title, jb.file)
	}
}

func runScheduled(jb *Job, c *cron.Cron) {
	if !jb.OnOff {
		infoLog.Printf("Skipping '%s'", jb.Title)
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
	infoLog.Printf("Running '%s'", jb.Title)
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
		errorLog.Printf("Error parsing command template '%s'-'%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, tr.cmd, err)
		return err
	}
	sb := new(strings.Builder)
	err = tmpl.Execute(sb, tr.cmdTemplateParams)
	if err != nil {
		errorLog.Printf("Error rendering command template '%s'-'%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, tr.cmd, err)
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
		errorLog.Printf("Error executing '%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, err)
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

func restartTaskRun(taskRun *TaskRun, jobRun *JobRun) {
	runTask(taskRun)
	updateJobRunStatusFromTasks(jobRun)
	//todo: add error check
}

func updateJobRunStatusFromTasks(jobRun *JobRun) {
	//todo: simplify
	for _, tr := range jobRun.TasksHistory {
		if tr.Status == RunFailure {
			jobRun.Status = RunFailure
			return
		} else if tr.Status == Running {
			jobRun.Status = Running
			return
		} else if tr.Status == NoRun {
			jobRun.Status = RunFailure
			return
		}
	}
	jobRun.Status = RunSuccess
}

func jobOnOff(jobidx int, JC *JobsAndCron) error {
	if jobidx >= len(JC.Jobs) || jobidx < 0 {
		errorLog.Printf("incorrect job index %v", jobidx)
		return errors.New("incorrect job index")
	}
	jb := JC.Jobs[jobidx]
	jb.OnOff = !jb.OnOff
	if jb.OnOff {
		jb.NextScheduled = JC.cron.Entry(jb.cronID).Next
	} else {
		jb.NextScheduled = time.Time{}
	}
	infoLog.Printf("Toggled state of %s to %v", jb.Title, jb.OnOff)
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
	http.HandleFunc("/", httpIndex)
	http.HandleFunc("/login", httpLogin)
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
	http.HandleFunc("/parsingerrors", httpParsingErrors)
	log.Fatal(http.ListenAndServe(CONF.port, nil))
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

func httpLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	var creds struct {
		Password string `json:"password"`
	}
	err := json.NewDecoder(r.Body).Decode(&creds)
	if err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}
	if creds.Password != CONF.password {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	expirationTime := time.Now().Add(15 * time.Minute)
	claims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(expirationTime),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(jwtSecretKey)
	if err != nil {
		http.Error(w, "Failed to create token", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    tokenString,
		Expires:  expirationTime,
		HttpOnly: true,
	})
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Login successful!"))
}

func httpCheckAuth(w http.ResponseWriter, r *http.Request) (error, int, string) {
	if CONF.password == "" {
		return nil, http.StatusOK, "Ok"
	}
	cookie, err := r.Cookie("token")
	if err != nil {
		if err == http.ErrNoCookie {
			return err, http.StatusUnauthorized, "Unauthorized"
		}
		return err, http.StatusBadRequest, "Bad request"
	}
	tokenStr := cookie.Value
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return jwtSecretKey, nil
	})
	if err != nil || !token.Valid {
		return err, http.StatusUnauthorized, "Unauthorized"
	}
	//todo: prolong token
	return nil, http.StatusOK, "Ok"
}

func httpJobs(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	jData, err := json.Marshal(JC)
	if err != nil {
		errorLog.Println(err)
		http.Error(w, "No Jobs Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jData)
}

func httpOnOff(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	err = jobOnOff(httpQPars.jobIndex, JC)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpRestart(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
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
		restartTaskRun(t, rn)
	} else if rn != nil {
		restartJobRun(jb, rn)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpRunNow(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	err = runNow(jb)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpLastOutput(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
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
	if t == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(t.lastOutput))
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

func httpParsingErrors(w http.ResponseWriter, r *http.Request) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	//todo: unnecessary conversions?
	w.Write([]byte(webLogBuf.String()))
}
