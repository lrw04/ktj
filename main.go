package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	// "fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	_ "modernc.org/sqlite"
)

type checker struct {
	Type   string `json:"type"`
	Source string `json:"source"`
}

type test struct {
	Input  string `json:"input"`
	Answer string `json:"answer"`
}

type problem struct {
	ID        string
	Title     string        `json:"title"`
	Statement template.HTML `json:"statements"`
	Size      string        `json:"size"`
	Memory    int64         `json:"memory"`
	Time      int64         `json:"time"`
	Checker   checker       `json:"checker"`
	Examples  []test        `json:"examples"`
	Tests     []test        `json:"tests"`
}

type config struct {
	JudgerKey  string            `json:"judger_key"`
	Listen     string            `json:"listen"`
	TimeZone   string            `json:"timezone"`
	Title      string            `json:"title"`
	Languages  map[string]string `json:"languages"`
}

func check(e error) {
	if e != nil {
		panic(e)
	}
}

//go:embed templates/*.html
var templates_fs embed.FS

//go:embed static
var static_files embed.FS

var wd string
var db *sql.DB
var cfg config
var probs map[string]problem
var templates = template.Must(template.New("templates").Funcs(template.FuncMap{
	"time_format": func(t time.Time) string {
		return t.Format("2006-01-02 15:04:05")
	},
}).ParseFS(templates_fs, "templates/*.html"))

func schema() {
	q := `create table if not exists submissions (
		id integer primary key autoincrement,
		user text not null,
		time date not null,
		language text not null,
		code text not null,
		problem text not null,
		verdict text not null,
		time_usage integer not null,
		memory_usage integer not null
	);`
	_, err := db.Exec(q)
	check(err)
}

func renderTemplate(w http.ResponseWriter, tmpl string, p any) {
	err := templates.ExecuteTemplate(w, tmpl+".html", p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type problem_list_data struct {
	Config   config
	Problems map[string]problem
}

func problem_list_page(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "home", problem_list_data{cfg, probs})
}

type problem_page_data struct {
	Config  config
	Problem problem
	Index   string
}

var last_submission time.Time

type rate_data struct {
	Config config
	Time   time.Time
}

func problem_page(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	prob := vars["problem"]
	if r.Method == "POST" {
		if time.Now().Before(last_submission.Add(time.Second * 5)) {
			renderTemplate(w, "rate", rate_data{cfg, last_submission.Add(time.Second * 5)})
			return
		}
		result, err := db.Exec(`insert into submissions (user, time, language, code, problem, verdict, time_usage, memory_usage) values (?, ?, ?, ?, ?, ?, ?, ?)`, r.FormValue("user"), time.Now(), r.FormValue("language"), r.FormValue("code"), prob, "pending", 0, 0)
		check(err)
		id, err := result.LastInsertId()
		check(err)
		http.Redirect(w, r, "/submission/"+strconv.Itoa(int(id)), http.StatusFound)
		last_submission = time.Now()
	}
	val, ok := probs[prob]
	if ok {
		renderTemplate(w, "problem", problem_page_data{cfg, val, prob})
		return
	}
	http.NotFound(w, r)
}

type submission struct {
	ID           int64
	User         string
	Time         time.Time
	Language     string
	LanguageName string
	Code         string
	Problem      string
	Verdict      string
	TimeUsage    int64
	MemoryUsage  int64
}

func getSubmission(id int64) (submission, error) {
	var sub submission
	err := db.QueryRow("select id, user, time, language, code, problem, verdict, time_usage, memory_usage from submissions where id = ?", id).Scan(&sub.ID, &sub.User, &sub.Time, &sub.Language, &sub.Code, &sub.Problem, &sub.Verdict, &sub.TimeUsage, &sub.MemoryUsage)
	if err != nil {
		return sub, err
	}
	sub.LanguageName = cfg.Languages[sub.Language]
	return sub, nil
}

type submission_page_data struct {
	Submission   submission
	Config       config
	ProblemTitle string
}

func submission_page(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["submission"], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sub, err := getSubmission(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	renderTemplate(w, "submission", submission_page_data{sub, cfg, probs[sub.Problem].Title})
}

type submission_listing struct {
	ID           int64
	User         string
	Time         time.Time
	Language     string
	LanguageName string
	Code         string
	Problem      string
	ProblemTitle string
	Verdict      string
	TimeUsage    int64
	MemoryUsage  int64
}

func getSubmissions() []submission_listing {
	last_submission = time.Now()
	rows, err := db.Query(`select id, user, time, language, code, problem, verdict, time_usage, memory_usage from submissions order by id desc`)
	check(err)
	l := make([]submission_listing, 0)
	for rows.Next() {
		var c submission_listing
		err := rows.Scan(&c.ID, &c.User, &c.Time, &c.Language, &c.Code, &c.Problem, &c.Verdict, &c.TimeUsage, &c.MemoryUsage)
		check(err)
		c.ProblemTitle = probs[c.Problem].Title
		c.LanguageName = cfg.Languages[c.Language]
		l = append(l, c)
	}
	err = rows.Err()
	check(err)
	return l
}

type submissions_page_data struct {
	Listing []submission_listing
	Config  config
}

func submissions_page(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "submissions", submissions_page_data{getSubmissions(), cfg})
}

func getPendingSubmission() (submission, error) {
	var sub submission
	err := db.QueryRow("select id from submissions where verdict = 'pending' order by id asc limit 1").Scan(&sub.ID)
	if err != nil {
		return sub, err
	}
	sub.LanguageName = cfg.Languages[sub.Language]
	return sub, nil
}

func setAssigned(id int64) {
	_, err := db.Exec("update submissions set verdict = 'assigned' where id = ?", id)
	check(err)
}

type solution struct {
	Language string `json:"language"`
	Source   string `json:"source"`
}

type assignment_problem struct {
	Size    string  `json:"size"`
	Memory  int64   `json:"memory"`
	Time    int64   `json:"time"`
	Checker checker `json:"checker"`
	Tests   []test  `json:"tests"`
}

type assignment struct {
	Solution solution           `json:"solution"`
	Problem  assignment_problem `json:"problem"`
}

func get_submission_handler(w http.ResponseWriter, r *http.Request) {
	sub, err := getPendingSubmission()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	setAssigned(sub.ID)
	sub, err = getSubmission(sub.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var a assignment
	a.Solution.Language = sub.Language
	a.Solution.Source = sub.Code
	a.Problem.Checker = probs[sub.Problem].Checker
	a.Problem.Memory = probs[sub.Problem].Memory
	a.Problem.Size = probs[sub.Problem].Size
	a.Problem.Tests = append(probs[sub.Problem].Examples, probs[sub.Problem].Tests...)
	a.Problem.Time = probs[sub.Problem].Time
	json.NewEncoder(w).Encode(a)
}

func update_submission_handler(w http.ResponseWriter, r *http.Request) {
}

func main() {
	wd = os.Args[1]
	var err error
	db, err = sql.Open("sqlite", path.Join(wd, "data.sqlite"))
	check(err)
	schema()

	probs_s, err := os.ReadFile(path.Join(wd, "problems.json"))
	check(err)
	err = json.Unmarshal(probs_s, &probs)
	check(err)
	cfg_s, err := os.ReadFile(path.Join(wd, "config.json"))
	check(err)
	err = json.Unmarshal(cfg_s, &cfg)
	check(err)

	r := mux.NewRouter()

	r.HandleFunc("/", problem_list_page).Methods("GET")
	r.HandleFunc("/{problem}", problem_page).Methods("GET", "POST")
	r.HandleFunc("/submission/{submission}", submission_page).Methods("GET")
	r.HandleFunc("/submission/", submissions_page).Methods("GET")
	r.HandleFunc("/api/v1/get-submission", get_submission_handler).Methods("GET")
	r.HandleFunc("/api/v1/update-submission", update_submission_handler).Methods("POST")
	r.PathPrefix("/").Handler(http.FileServer(http.FS(static_files)))

	http.ListenAndServe(cfg.Listen, r)
}
