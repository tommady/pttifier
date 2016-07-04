package main

import (
	"encoding/json"
	ptylib "github.com/tommady/pttifierLib"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type status ptylib.BaseInfo

type config struct {
	// Minute as unit
	CrawlingPeriod int    `json:"crawling_period"`
	RulePath       string `json:"rule_path"`
	StatusPath     string `json:"status_path"`
	ResultPath     string `json:"result_path"`
}

type rule struct {
	Board          string   `json:"board"`
	BoardKeyword   []string `json:"board_keyword"`
	AuthorKeyword  []string `json:"author_keyword"`
	ContentKeyword []string `json:"content_keyword"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	conf, err := loadConfig("./config.json")
	if err != nil {
		log.Fatalln(err)
	}
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		signal.Stop(sigs)
		log.Println(sig)
		done <- true
	}()
	if err := startParsing(conf); err != nil {
		log.Println(err)
	}
MainLoop:
	for {
		select {
		case <-done:
			log.Println("exiting")
			break MainLoop
		case <-time.After(time.Minute * time.Duration(conf.CrawlingPeriod)):
			log.Println("starting")
			if err := startParsing(conf); err != nil {
				log.Println(err)
			}
		}
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
	stats, err := loadStatus(statusFilePath + r.Board + ".json")
	if err != nil {
		log.Println(err)
		return nil
	}
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
	retPosts := []*ptylib.BoardInfoAndArticle{}
POST_LOOP:
	for {
		for _, post := range posts {
			if stats.URL == post.URL {
				break POST_LOOP
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
		setStatus(statusFilePath+r.Board+".json",
			&status{
				URL:    retPosts[0].BaseInfo.URL,
				Title:  retPosts[0].BaseInfo.Title,
				Author: retPosts[0].BaseInfo.Author,
				Date:   retPosts[0].BaseInfo.Date,
			})
	}
	log.Println("setting done")
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

func loadStatus(path string) (*status, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := new(status)
	decoder := json.NewDecoder(f)
	if err = decoder.Decode(&s); err != nil {
		return nil, err
	}
	return s, nil
}

func setStatus(path string, stats *status) error {
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
