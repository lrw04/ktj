package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

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

type contest struct {
	Title     string            `json:"title"`
	Start     string            `json:"start"`
	Duration  int64             `json:"duration"` // minutes
	Freeze    int64             `json:"freeze"`   // minutes
	Problems  []problem         `json:"problems"`
	Languages map[string]string `json:"languages"`
}

type config struct {
	SessionKey string `json:"session_key"`
	JudgerKey  string `json:"judger_key"`
	Listen     string `json:"listen"`
	TimeZone   string `json:"timezone"`
}

type session struct {
	User string `json:"user"`
	Salt string `json:"salt"`
}

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	s := make([]rune, n)
	for i := range s {
		ind, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		check(err)
		s[i] = letters[ind.Int64()]
	}
	return string(s)
}

var wd string
var db *sql.DB
var contest_content contest
var users map[string]string
var cfg config
var startTime, freezeTime, endTime time.Time
var aeskey [32]byte
var sessionCoding cipher.Block

func encodeSession(s session) string {
	b, err := json.Marshal(s)
	check(err)
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		panic(err)
	}
	stream := cipher.NewCFBEncrypter(sessionCoding, iv)
	stream.XORKeyStream(ciphertext[aes.BlockSize:], b)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func decodeSession(s string) session {
	sess := session{}
	orig, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return session{}
	}
	if len(orig) < aes.BlockSize {
		return session{}
	}
	iv := orig[:aes.BlockSize]
	orig = orig[aes.BlockSize:]
	stream := cipher.NewCFBDecrypter(sessionCoding, iv)
	stream.XORKeyStream(orig, orig)
	err = json.Unmarshal(orig, &sess)
	if err != nil {
		return session{}
	}
	return sess
}

func getSession(r *http.Request) session {
	s, err := r.Cookie("session")
	if err != nil {
		return session{}
	}
	sess := decodeSession(s.Value)
	return sess
}

var templates = template.Must(template.New("templates").Funcs(template.FuncMap{
	"time_format": func(t time.Time) string {
		return t.Format("2006-01-02 15:04:05")
	},
}).ParseFiles(
	"templates/home.html",
	"templates/login.html",
	"templates/login-fail.html",
	"templates/problem.html",
	"templates/standings.html",
	"templates/submission.html",
	"templates/submissions.html",
	"templates/not-started.html",
))

func renderTemplate(w http.ResponseWriter, tmpl string, p any) {
	err := templates.ExecuteTemplate(w, tmpl+".html", p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func notStarted() bool {
	return time.Now().Before(startTime)
}

func rankingFrozen(t time.Time) bool {
	return t.After(freezeTime)
}

func contestEnded(t time.Time) bool {
	return t.After(endTime)
}

type problem_list_template struct {
	Session session
	Contest contest
}

func problemList(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", contest_content)
		return
	}
	renderTemplate(w, "home", problem_list_template{getSession(r), contest_content})
}

type problem_page_template struct {
	ContestTitle string
	Problem      problem
	Index        string
	Session      session
	Contest      contest
}

func problemPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", contest_content)
		return
	}
	// /problem/
	prob_part := r.URL.Path[9:]
	if len(prob_part) != 1 {
		http.NotFound(w, r)
		return
	}
	prob_index := int(prob_part[0] - 'A')
	if prob_index < 0 || prob_index >= len(contest_content.Problems) {
		http.NotFound(w, r)
		return
	}
	renderTemplate(w, "problem", problem_page_template{contest_content.Title, contest_content.Problems[prob_index], prob_part, getSession(r), contest_content})
}

type submission struct {
	ID           int64
	User         string
	Time         time.Time
	Language     string
	LanguageName string
	Code         string
	ProblemIndex int64
	Verdict      string
	TimeUsage    int64
	MemoryUsage  int64
}

func getSubmission(id int64) (submission, error) {
	var sub submission
	var problem string
	err := db.QueryRow("select id, user, time, language, code, problem, verdict, time_usage, memory_usage from submissions where id = ?", id).Scan(&sub.ID, &sub.User, &sub.Time, &sub.Language, &sub.Code, &problem, &sub.Verdict, &sub.TimeUsage, &sub.MemoryUsage)
	sub.LanguageName = contest_content.Languages[sub.Language]
	if err != nil {
		return sub, err
	}
	sub.ProblemIndex = int64(problem[0] - 'A')
	return sub, nil
}

type submission_page_template struct {
	Session      session
	Submission   submission
	Contest      contest
	Problem      string
	ProblemTitle string
	StatusShown  bool
	CodeShown    bool
}

func submissionPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", contest_content)
		return
	}
	// /submission/
	ids := r.URL.Path[12:]
	id, err := strconv.ParseInt(ids, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sub, err := getSubmission(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	renderTemplate(w, "submission", submission_page_template{getSession(r), sub, contest_content, string('A' + byte(sub.ProblemIndex)), contest_content.Problems[sub.ProblemIndex].Title, (!rankingFrozen(sub.Time)) || contestEnded(time.Now()) || getSession(r).User == sub.User, contestEnded(time.Now()) || getSession(r).User == sub.User})
}

func submissionHandler(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", contest_content)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	// /submit/
	problem := r.URL.Path[8:]
	if len(problem) > 1 {
		http.NotFound(w, r)
		return
	}
	result, err := db.Exec(`insert into submissions (user, time, language, code, problem, verdict, time_usage, memory_usage) values (?, ?, ?, ?, ?, ?, ?, ?)`, getSession(r).User, time.Now(), r.FormValue("language"), r.FormValue("code"), problem, "pending", 0, 0)
	check(err)
	id, err := result.LastInsertId()
	check(err)
	http.Redirect(w, r, "/submission/"+strconv.Itoa(int(id)), http.StatusFound)
}

type submission_listing struct {
	ID           int64
	User         string
	Time         time.Time
	Language     string
	LanguageName string
	Code         string
	Problem      string
	ProblemIndex int64
	ProblemTitle string
	Verdict      string
	TimeUsage    int64
	MemoryUsage  int64
	StatusShown  bool
}

func getSubmissions() []submission_listing {
	rows, err := db.Query(`select id, user, time, language, code, problem, verdict, time_usage, memory_usage from submissions order by id desc`)
	check(err)
	l := make([]submission_listing, 0)
	for rows.Next() {
		var c submission_listing
		err := rows.Scan(&c.ID, &c.User, &c.Time, &c.Language, &c.Code, &c.Problem, &c.Verdict, &c.TimeUsage, &c.MemoryUsage)
		check(err)
		c.ProblemIndex = int64(c.Problem[0] - 'A')
		c.ProblemTitle = contest_content.Problems[c.ProblemIndex].Title
		c.LanguageName = contest_content.Languages[c.Language]
		l = append(l, c)
	}
	err = rows.Err()
	check(err)
	return l
}

type submissions_page_template struct {
	Listing []submission_listing
	Contest contest
	Session session
}

func submissionsPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", contest_content)
		return
	}
	cont := submissions_page_template{getSubmissions(), contest_content, getSession(r)}
	for i := 0; i < len(cont.Listing); i++ {
		cont.Listing[i].StatusShown = (!rankingFrozen(cont.Listing[i].Time)) || contestEnded(time.Now()) || getSession(r).User == cont.Listing[i].User
	}
	renderTemplate(w, "submissions", cont)
}

// TODO
func standingsPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", contest_content)
		return
	}
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s, err := r.Cookie("session")
		if err != nil {
			renderTemplate(w, "login", contest_content.Title)
			return
		}
		sess := decodeSession(s.Value)
		if len(sess.User) != 0 {
			http.Redirect(w, r, "/", http.StatusFound)
		}
		renderTemplate(w, "login", contest_content.Title)
		return
	}
	if users[r.FormValue("username")] == r.FormValue("password") && len(users[r.FormValue("username")]) > 0 {
		sess := session{r.FormValue("username"), randomString(32)}
		expiration := time.Now()
		expiration = expiration.Add(time.Hour)
		http.SetCookie(w, &http.Cookie{Name: "session", Value: encodeSession(sess), Expires: expiration, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusFound)
	} else {
		renderTemplate(w, "login-fail", contest_content.Title)
	}
}

// TODO
func getTaskHandler(w http.ResponseWriter, r *http.Request) {
}

// TODO
func updateTaskHandler(w http.ResponseWriter, r *http.Request) {
}

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
	);
	create table if not exists standings (
		user text not null,
		score integer not null,
		penalty integer not null,
		status text not null
	);
	create table if not exists standings_disp (
		user text not null,
		score integer not null,
		penalty integer not null,
		status text not null
	);`
	_, err := db.Exec(q)
	check(err)
}

func main() {
	wd = os.Args[1]
	var err error
	db, err = sql.Open("sqlite", path.Join(wd, "data.sqlite"))
	schema()

	check(err)
	probs_s, err := os.ReadFile(path.Join(wd, "contest.json"))
	check(err)
	json.Unmarshal(probs_s, &contest_content)
	users_s, err := os.ReadFile(path.Join(wd, "users.json"))
	check(err)
	json.Unmarshal(users_s, &users)
	cfg_s, err := os.ReadFile(path.Join(wd, "config.json"))
	check(err)
	json.Unmarshal(cfg_s, &cfg)

	loc, err := time.LoadLocation(cfg.TimeZone)
	check(err)
	startTime, err = time.ParseInLocation("2006-01-02 15:04", contest_content.Start, loc)
	check(err)
	freezeTime = startTime.Add(time.Minute * time.Duration(contest_content.Freeze))
	endTime = startTime.Add(time.Minute * time.Duration(contest_content.Duration))

	aeskey = sha256.Sum256([]byte(cfg.SessionKey))
	sessionCoding, err = aes.NewCipher(aeskey[:])
	check(err)

	s := 'A'
	for i := 0; i < len(contest_content.Problems); i++ {
		contest_content.Problems[i].ID = string(s)
		s++
	}

	// GET /
	http.HandleFunc("/", problemList)
	// GET /problem/{id}
	http.HandleFunc("/problem/", problemPage)
	// POST /submit/{id}
	http.HandleFunc("/submit/", submissionHandler)
	// GET /submission/{id}
	http.HandleFunc("/submission/", submissionPage)
	// GET /submissions
	http.HandleFunc("/submissions", submissionsPage)
	// GET /standings
	http.HandleFunc("/standings", standingsPage)
	// GET, POST /login
	http.HandleFunc("/login", loginHandler)
	// GET /api/v1/task
	http.HandleFunc("/api/v1/task", getTaskHandler)
	// POST /api/v1/update-task
	http.HandleFunc("/api/v1/update-task", updateTaskHandler)
	fs := http.FileServer(http.Dir("static/"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))
	log.Fatal(http.ListenAndServe(cfg.Listen, nil))
}
