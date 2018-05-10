package main

import (
	"crypto/sha1"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/unrolled/render"
)

type Tweet struct {
	ID        int
	UserID    int
	Text      string
	CreatedAt time.Time
}

type DispTweet struct {
	UserName string
	HTML     string
	Time     string
}

type User struct {
	ID       int
	Name     string
	Salt     string
	Password string
}

const (
	sessionName     = "isuwitter_session"
	sessionSecret   = "isuwitter"
	perPage         = 50
	isutomoEndpoint = "http://localhost:8081"
)

var (
	re             *render.Render
	store          *sessions.FilesystemStore
	db             *sql.DB
	errInvalidUser = errors.New("Invalid User")
)

func getuserID(name string) int {
	row := db.QueryRow(`SELECT id FROM users WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID)
	if err != nil {
		return 0
	}
	return user.ID
}

func getUserName(id int) string {
	row := db.QueryRow(`SELECT name FROM users WHERE id = ?`, id)
	user := User{}
	err := row.Scan(&user.Name)
	if err != nil {
		return ""
	}
	return user.Name
}

func htmlify(tweet string) string {
	tweet = strings.Replace(tweet, "&", "&amp;", -1)
	tweet = strings.Replace(tweet, "<", "&lt;", -1)
	tweet = strings.Replace(tweet, ">", "&gt;", -1)
	tweet = strings.Replace(tweet, "'", "&apos;", -1)
	tweet = strings.Replace(tweet, "\"", "&quot;", -1)
	re := regexp.MustCompile("#(\\S+)(\\s|$)")
	tweet = re.ReplaceAllStringFunc(tweet, func(tag string) string {
		return fmt.Sprintf("<a class=\"hashtag\" href=\"/hashtag/%s\">#%s</a>", tag[1:len(tag)], html.EscapeString(tag[1:len(tag)]))
	})
	return tweet
}

func loadFriends(userID int) ([]int, error) {
	friends := make([]int, 0)
	rows, err := db.Query("SELECT friend_id FROM friends WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		id := 0
		err := rows.Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		friends = append(friends, id)
	}
	return friends, nil
}

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM tweets WHERE id > 100000`)
	if err != nil {
		badRequest(w)
		fmt.Println(err.Error())
		return
	}

	_, err = db.Exec(`DELETE FROM users WHERE id > 1000`)
	if err != nil {
		badRequest(w)
		fmt.Println(err.Error())
		return
	}

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func topHandler(w http.ResponseWriter, r *http.Request) {
	var name string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(userID.(int))
	}

	if name == "" {
		flush, _ := session.Values["flush"].(string)
		session := getSession(w, r)
		session.Options = &sessions.Options{MaxAge: -1}
		session.Save(r, w)

		re.HTML(w, http.StatusOK, "index", struct {
			Name  string
			Flush string
		}{
			name,
			flush,
		})
		return
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT id, user_id, text, created_at FROM tweets WHERE user_id IN (SELECT friend_id FROM friends WHERE user_id=?) ORDER BY tweets.id DESC LIMIT ?`, userID.(int), perPage)
	} else {
		rows, err = db.Query(`SELECT id, user_id, text, created_at FROM tweets WHERE user_id IN (SELECT friend_id FROM friends WHERE user_id=?) AND created_at < ? ORDER BY tweets.id DESC LIMIT ?`, userID.(int), until, perPage)
	}

	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		fmt.Println(err.Error())
		return
	}
	defer rows.Close()

	tweets := make([]*DispTweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.ID, &t.UserID, &t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			fmt.Println(err.Error())
			return
		}

		dtw := DispTweet{"", htmlify(t.Text), t.CreatedAt.Format("2006-01-02 15:04:05")}
		dtw.UserName = getUserName(t.UserID)
		if dtw.UserName == "" {
			badRequest(w)
			fmt.Println("non username")
			return
		}
		tweets = append(tweets, &dtw)
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*DispTweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "index", struct {
		Name   string
		Tweets []*DispTweet
	}{
		name, tweets,
	})
}

func tweetPostHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		u := getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	text := r.FormValue("text")
	if text == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	_, err := db.Exec(`INSERT INTO tweets (user_id, text, created_at) VALUES (?, ?, NOW())`, userID, text)
	if err != nil {
		badRequest(w)
		fmt.Println(err.Error())
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	row := db.QueryRow(`SELECT * FROM users WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID, &user.Name, &user.Salt, &user.Password)
	if err != nil && err != sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err == sql.ErrNoRows || user.Password != fmt.Sprintf("%x", sha1.Sum([]byte(user.Salt+r.FormValue("password")))) {
		session := getSession(w, r)
		session.Values["flush"] = "ログインエラー"
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	session := getSession(w, r)
	session.Values["user_id"] = user.ID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func followHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	_, err := db.Exec(`INSERT INTO friends (user_id, friend_id) VALUES (?, ?)`, userID, getuserID(r.FormValue("user")))
	if err != nil {
		badRequest(w)
		fmt.Println(err.Error())
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

func unfollowHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	_, err := db.Exec(`DELETE FROM friends WHERE user_id = ? AND friend_id = ?`, userID, getuserID(r.FormValue("user")))
	if err != nil {
		badRequest(w)
		fmt.Println(err.Error())
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)

	return session
}

func pathURIEscape(s string) string {
	return (&url.URL{Path: s}).String()
}

func badRequest(w http.ResponseWriter) {
	code := http.StatusBadRequest
	http.Error(w, http.StatusText(code), code)
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	var name string
	session := getSession(w, r)
	sessionUID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(sessionUID.(int))
	} else {
		name = ""
	}

	user := mux.Vars(r)["user"]
	mypage := user == name

	userID := getuserID(user)
	if userID == 0 {
		http.NotFound(w, r)
		return
	}

	isFriend := false
	var rows *sql.Rows
	var err error
	if name != "" {
		rows, err := db.Query(`SELECT id FROM friends WHERE user_id = ? AND friend_id = ?`, sessionUID.(int), userID)
		if err != nil {
			if err == sql.ErrNoRows {
				http.NotFound(w, r)
				return
			}
			badRequest(w)
			fmt.Println(err.Error())
			return
		}
		for rows.Next() {
			id := 0
			err := rows.Scan(&id)
			if err != nil && err != sql.ErrNoRows {
				badRequest(w)
				fmt.Println(err.Error())
				return
			}
			if 0 != id {
				isFriend = true
			}
		}
	}

	until := r.URL.Query().Get("until")
	if until == "" {
		rows, err = db.Query(`SELECT * FROM tweets WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, perPage)
	} else {
		rows, err = db.Query(`SELECT * FROM tweets WHERE user_id = ? AND created_at < ? ORDER BY created_at DESC LIMIT ?`, userID, until, perPage)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		fmt.Println(err.Error())
		return
	}
	defer rows.Close()

	tweets := make([]*DispTweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.ID, &t.UserID, &t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			fmt.Println(err.Error())
			return
		}

		tweets = append(tweets, &DispTweet{user, htmlify(t.Text), t.CreatedAt.Format("2006-01-02 15:04:05")})
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*DispTweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "user", struct {
		Name     string
		User     string
		Tweets   []*DispTweet
		IsFriend bool
		Mypage   bool
	}{
		name, user, tweets, isFriend, mypage,
	})
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	var name string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(userID.(int))
	} else {
		name = ""
	}

	query := r.URL.Query().Get("q")
	if mux.Vars(r)["tag"] != "" {
		query = "#" + mux.Vars(r)["tag"]
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT * FROM tweets WHERE text LIKE ? ORDER BY id DESC LIMIT ?`, "%"+query+"%", perPage)
	} else {
		rows, err = db.Query(`SELECT * FROM tweets WHERE created_at < ? AND text LIKE ? ORDER BY created_at DESC LIMIT ?`, until, "%"+query+"%", perPage)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		fmt.Println(err.Error())
		return
	}
	defer rows.Close()

	tweets := make([]*DispTweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.ID, &t.UserID, &t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			fmt.Println(err.Error())
			return
		}
		dtw := DispTweet{"", htmlify(t.Text), t.CreatedAt.Format("2006-01-02 15:04:05")}
		dtw.UserName = getUserName(t.UserID)
		if dtw.UserName == "" {
			badRequest(w)
			fmt.Println("non username")
			return
		}
		tweets = append(tweets, &dtw)
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*DispTweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "search", struct {
		Name   string
		Tweets []*DispTweet
		Query  string
	}{
		name, tweets, query,
	})
}

func fileRead(fp string) []byte {
	fs, err := os.Open(fp)

	if err != nil {
		return nil
	}

	defer fs.Close()

	l, err := fs.Stat()

	if err != nil {
		return nil
	}

	buf := make([]byte, l.Size())

	_, err = fs.Read(buf)

	if err != nil {
		return nil
	}

	return buf
}

func main() {
	host := os.Getenv("ISUWITTER_DB_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("ISUWITTER_DB_PORT")
	if port == "" {
		port = "3306"
	}
	user := os.Getenv("ISUWITTER_DB_USER")
	if user == "" {
		user = "root"
	}
	password := os.Getenv("ISUWITTER_DB_PASSWORD")
	dbname := os.Getenv("ISUWITTER_DB_NAME")
	if dbname == "" {
		dbname = "isuwitter"
	}

	var err error
	db, err = sql.Open("mysql", fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&loc=Local&parseTime=true",
		user, password, host, port, dbname,
	))
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}

	store = sessions.NewFilesystemStore("", []byte(sessionSecret))

	re = render.New(render.Options{
		Directory: "views",
		Funcs: []template.FuncMap{
			{
				"raw": func(text string) template.HTML {
					return template.HTML(text)
				},
				"add": func(a, b int) int { return a + b },
			},
		},
	})

	r := mux.NewRouter()
	r.HandleFunc("/initialize", initializeHandler).Methods("GET")

	l := r.PathPrefix("/login").Subrouter()
	l.Methods("POST").HandlerFunc(loginHandler)
	r.HandleFunc("/logout", logoutHandler)

	s := r.PathPrefix("/search").Subrouter()
	s.Methods("GET").HandlerFunc(searchHandler)
	t := r.PathPrefix("/hashtag/{tag}").Subrouter()
	t.Methods("GET").HandlerFunc(searchHandler)

	n := r.PathPrefix("/unfollow").Subrouter()
	n.Methods("POST").HandlerFunc(unfollowHandler)
	f := r.PathPrefix("/follow").Subrouter()
	f.Methods("POST").HandlerFunc(followHandler)

	u := r.PathPrefix("/{user}").Subrouter()
	u.Methods("GET").HandlerFunc(userHandler)

	i := r.PathPrefix("/").Subrouter()
	i.Methods("GET").HandlerFunc(topHandler)
	i.Methods("POST").HandlerFunc(tweetPostHandler)

	log.Fatal(http.ListenAndServe(":8080", r))
}
