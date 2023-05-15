package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path"
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
	Title    string    `json:"title"`
	Start    string    `json:"start"`
	Duration int64     `json:"duration"` // minutes
	Freeze   int64     `json:"freeze"`   // minutes
	Problems []problem `json:"problems"`
}

type config struct {
	SessionKey string `json:"session_key"`
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
var probs contest
var users map[string]string
var cfg config
var startTime, freezeTime, endTime time.Time
var aeskey [32]byte
var sessionCoding cipher.Block

func encodeSession(s session) string {
	b, err := json.Marshal(s)
	fmt.Println(b)
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

var templates = template.Must(template.ParseFiles(
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

func rankingFrozen() bool {
	return time.Now().Before(freezeTime)
}

func contestEnded() bool {
	return time.Now().Before(endTime)
}

type problem_list_template struct {
	Session session
	Contest contest
}

func problemList(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", probs)
		return
	}
	renderTemplate(w, "home", problem_list_template{getSession(r), probs})
}

type problem_page_template struct {
	ContestTitle string
	Problem      problem
	Index        string
	Session      session
}

func problemPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", probs)
		return
	}
	// /problem/
	prob_part := r.URL.Path[9:]
	if len(prob_part) != 1 {
		http.NotFound(w, r)
		return
	}
	prob_index := int(prob_part[0] - 'A')
	if prob_index < 0 || prob_index >= len(probs.Problems) {
		http.NotFound(w, r)
		return
	}
	fmt.Println(prob_part)
	renderTemplate(w, "problem", problem_page_template{probs.Title, probs.Problems[prob_index], prob_part, getSession(r)})
}

// TODO
func submissionPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", probs)
		return
	}
}

// TODO
func submissionHandler(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", probs)
		return
	}
}

// TODO
func submissionsPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", probs)
		return
	}
}

// TODO
func standingsPage(w http.ResponseWriter, r *http.Request) {
	if notStarted() {
		renderTemplate(w, "not-started", probs)
		return
	}
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s, err := r.Cookie("session")
		if err != nil {
			renderTemplate(w, "login", probs.Title)
			return
		}
		sess := decodeSession(s.Value)
		if len(sess.User) != 0 {
			http.Redirect(w, r, "/", http.StatusFound)
		}
		renderTemplate(w, "login", probs.Title)
		return
	}
	if users[r.FormValue("username")] == r.FormValue("password") && len(users[r.FormValue("username")]) > 0 {
		sess := session{r.FormValue("username"), randomString(32)}
		expiration := time.Now()
		expiration = expiration.Add(time.Hour)
		http.SetCookie(w, &http.Cookie{Name: "session", Value: encodeSession(sess), Expires: expiration, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusFound)
	} else {
		renderTemplate(w, "login-fail", probs.Title)
	}
}

// TODO
func getTaskHandler(w http.ResponseWriter, r *http.Request) {
}

// TODO
func updateTaskHandler(w http.ResponseWriter, r *http.Request) {
}

func main() {
	wd = os.Args[1]
	var err error
	db, err = sql.Open("sqlite", path.Join(wd, "data.sqlite"))
	check(err)
	probs_s, err := os.ReadFile(path.Join(wd, "contest.json"))
	check(err)
	json.Unmarshal(probs_s, &probs)
	users_s, err := os.ReadFile(path.Join(wd, "users.json"))
	check(err)
	json.Unmarshal(users_s, &users)
	cfg_s, err := os.ReadFile(path.Join(wd, "config.json"))
	check(err)
	json.Unmarshal(cfg_s, &cfg)

	loc, err := time.LoadLocation(cfg.TimeZone)
	check(err)
	startTime, err = time.ParseInLocation("2006/01/02 15:04", probs.Start, loc)
	check(err)
	freezeTime = startTime.Add(time.Minute * time.Duration(probs.Freeze))
	endTime = startTime.Add(time.Minute * time.Duration(probs.Duration))

	aeskey = sha256.Sum256([]byte(cfg.SessionKey))
	sessionCoding, err = aes.NewCipher(aeskey[:])
	check(err)

	s := 'A'
	for i := 0; i < len(probs.Problems); i++ {
		probs.Problems[i].ID = string(s)
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
