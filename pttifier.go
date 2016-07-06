package main

import (
	"encoding/json"
	ptylib "github.com/tommady/pttifierLib"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type config struct {
	// Minute as unit
	CrawlingPeriod int    `json:"crawling_period"`
	RulePath       string `json:"rule_path"`
	StatusPath     string `json:"status_path"`
	ResultPath     string `json:"result_path"`
	ListenPort     string `json:"listen_port"`
}

type rule struct {
	Board          string   `json:"board"`
	BoardKeyword   []string `json:"board_keyword"`
	AuthorKeyword  []string `json:"author_keyword"`
	ContentKeyword []string `json:"content_keyword"`
}

type baseHandler struct {
	*config
	H func(conf *config, w http.ResponseWriter, r *http.Request)
}

// ServeHTTP allows our Handler type to satisfy http.Handler.
func (h baseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.H(h.config, w, r)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	conf, err := loadConfig("./config.json")
	if err != nil {
		log.Fatalln(err)
	}
	go func() {
		for {
			select {
			case <-time.After(time.Minute * time.Duration(conf.CrawlingPeriod)):
				log.Println("starting")
				if err := startParsing(conf); err != nil {
					log.Println(err)
				}
			}
		}
	}()
	http.Handle("/", baseHandler{config: conf, H: rootHandler})
	http.Handle("/view", baseHandler{config: conf, H: viewHandler})
	http.Handle("/delete", baseHandler{config: conf, H: deleteHandler})
	log.Fatal(http.ListenAndServe(conf.ListenPort, nil))
}

type byDate []os.FileInfo

func (fs byDate) Len() int           { return len(fs) }
func (fs byDate) Swap(i, j int)      { fs[i], fs[j] = fs[j], fs[i] }
func (fs byDate) Less(i, j int) bool { return fs[i].ModTime().Before(fs[j].ModTime()) }

func rootHandler(conf *config, w http.ResponseWriter, r *http.Request) {
	files, err := ioutil.ReadDir(conf.ResultPath)
	if err != nil {
		log.Println(err)
		return
	}
	sortFiles := make(byDate, 0, len(files))
	for _, f := range files {
		sortFiles = append(sortFiles, f)
	}
	sort.Sort(sortFiles)
	page := template.Must(template.ParseFiles(
		"index.tmpl",
	))
	if err := page.Execute(w, sortFiles); err != nil {
		log.Println(err)
		return
	}
}

func deleteHandler(conf *config, w http.ResponseWriter, r *http.Request) {
	path := conf.ResultPath + r.FormValue("title")
	if err := os.Remove(path); err != nil {
		log.Println(err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func viewHandler(conf *config, w http.ResponseWriter, r *http.Request) {
	path := conf.ResultPath + r.FormValue("title")
	f, err := os.Open(path)
	if err != nil {
		log.Println(err)
		return
	}
	defer f.Close()
	cs := new(ptylib.BoardInfoAndArticle)
	decoder := json.NewDecoder(f)
	if err = decoder.Decode(&cs); err != nil {
		log.Println(err)
		return
	}
	page := template.Must(template.ParseFiles(
		"content.tmpl",
	))
	if err := page.Execute(w, cs); err != nil {
		log.Println(err)
		return
	}
}

func startParsing(conf *config) error {
	rules, err := loadRules(conf.RulePath)
	if err != nil {
		return err
	}
	for _, r := range rules {
		go parsing(r, conf)
	}
	return nil
}

func parsing(r *rule, conf *config) {
	posts := collectPosts(r, conf.StatusPath)
	if posts == nil {
		log.Println("get none posts")
		return
	}
	parsers := []*ptylib.Parser{}
	for _, parserTitle := range r.BoardKeyword {
		parsers = append(parsers, ptylib.NewParser(
			ptylib.SetParserTitle(parserTitle),
		))
	}
	results := []*ptylib.BoardInfoAndArticle{}
	resultCh := make(chan []*ptylib.BoardInfoAndArticle, len(parsers))
	for _, parser := range parsers {
		go func(parser *ptylib.Parser) {
			rs := parser.ParsingAll(posts)
			resultCh <- rs
		}(parser)
	}
	for i := 0; i < len(parsers); i++ {
		select {
		case rs := <-resultCh:
			results = append(results, rs...)
		}
	}
	for _, r := range results {
		setResult(conf.ResultPath+r.Title+".json", r)
	}
}

func collectPosts(r *rule, statusFilePath string) []*ptylib.BoardInfoAndArticle {
	link := ptylib.WrapBoardPageLink(r.Board, "")
	root, err := ptylib.GetNodeFromLink(link)
	if err != nil {
		log.Println(err)
		return nil
	}
	board := ptylib.NewBoardCrawler(root)
	posts := board.GetPostsInfosAndArticles()
	if board.Err() != nil {
		log.Println(board.Err())
		return nil
	}
	stats, err := loadStatus(statusFilePath + r.Board + ".json")
	if err == io.EOF {
		sts := []*ptylib.BaseInfo{}
		for _, p := range posts {
			st := new(ptylib.BaseInfo)
			st.Author = p.Author
			st.Date = p.Date
			st.Title = p.Title
			st.URL = p.URL
			sts = append(sts, st)
		}
		setStatus(statusFilePath+r.Board+".json", sts)
		return posts
	} else if err != nil {
		log.Println(err)
		return nil
	}
	retPosts := []*ptylib.BoardInfoAndArticle{}
POST_LOOP:
	for {
		for _, post := range posts {
			for _, sts := range stats {
				if sts.URL == post.URL {
					break POST_LOOP
				}
			}
			retPosts = append(retPosts, post)
		}
		root, err := ptylib.GetNodeFromLink(board.GetPrevPageLink())
		if err != nil {
			log.Println(err)
			return nil
		}
		board = ptylib.NewBoardCrawler(root)
		posts = board.GetPostsInfosAndArticles()
		if board.Err() != nil {
			log.Println(board.Err())
			return nil
		}
		time.Sleep(time.Second * 1)
	}
	if len(retPosts) != 0 {
		sts := []*ptylib.BaseInfo{}
		for _, r := range retPosts {
			st := new(ptylib.BaseInfo)
			st.Author = r.Author
			st.Date = r.Date
			st.Title = r.Title
			st.URL = r.URL
			sts = append(sts, st)
		}
		setStatus(statusFilePath+r.Board+".json", sts)
	}
	return retPosts
}

func loadConfig(path string) (*config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := new(config)
	decoder := json.NewDecoder(f)
	if err = decoder.Decode(&c); err != nil {
		return nil, err
	}
	return c, nil
}

func loadRules(path string) ([]*rule, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := []*rule{}
	decoder := json.NewDecoder(f)
	if err = decoder.Decode(&r); err != nil {
		return nil, err
	}
	return r, nil
}

func loadStatus(path string) ([]*ptylib.BaseInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") ||
			strings.Contains(err.Error(), "The system cannot find the file specified.") {
			f, err = os.Create(path)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	defer f.Close()
	s := []*ptylib.BaseInfo{}
	decoder := json.NewDecoder(f)
	if err = decoder.Decode(&s); err != nil {
		return nil, err
	}
	return s, nil
}

func setStatus(path string, stats []*ptylib.BaseInfo) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(stats); err != nil {
		return err
	}
	return nil
}

func setResult(path string, result *ptylib.BoardInfoAndArticle) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(result); err != nil {
		return err
	}
	return nil
}
