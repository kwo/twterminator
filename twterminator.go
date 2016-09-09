package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/ChimeraCoder/anaconda"
	"gopkg.in/yaml.v2"
	"io"
	"net/url"
	"os"
	"path"
	"sync"
	"time"
)

const (
	configFileName = ".twterminator.yaml"
	maxErrorCount  = 3
)

// Tweet types
const (
	Tweet = "Tweet"
	Like  = "Like"
)

var (
	debug   = flag.Bool("d", false, "debug messages on")
	xoxo    = flag.Bool("x", false, "commit changes (default is dry-run)")
	backlog = flag.Int("b", 0, "backlog days, override max days from configuration file")
	cfg     *Configuration
	twitter *anaconda.TwitterApi
	latch   = sync.WaitGroup{}
)

// Configuration object
type Configuration struct {
	Auth   AuthInfo
	Filter FilterInfo
}

// AuthInfo object
type AuthInfo struct {
	ConsumerKey    string
	ConsumerSecret string
	AccessToken    string
	AccessSecret   string
	Username       string
}

// FilterInfo object
type FilterInfo struct {
	BacklogDays int
}

// Load configuration from JSON
func (z *Configuration) Load(data []byte) error {
	return yaml.Unmarshal(data, z)
}

// LoadFromReader configuration from JSON
func (z *Configuration) LoadFromReader(r io.ReadCloser) error {
	var b bytes.Buffer
	b.ReadFrom(r)
	r.Close()
	return z.Load(b.Bytes())
}

// LoadFromFile configuration from JSON
func (z *Configuration) LoadFromFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	return z.LoadFromReader(f)
}

// GetConfig get the configurtion
func GetConfig() *Configuration {
	cfg := Configuration{}
	if err := cfg.LoadFromFile(GetConfigFileLocation()); err != nil {
		return nil
	}
	return &cfg
}

// GetConfigFileLocation get the location of the config file
func GetConfigFileLocation() string {
	if home := GetHomeDirectory(); home != "" {
		return path.Join(GetHomeDirectory(), configFileName)
	}
	return configFileName
}

// GetHomeDirectory get the user home directory
func GetHomeDirectory() string {
	homeLocations := []string{"HOME", "HOMEPATH", "USERPROFILE"}
	for _, v := range homeLocations {
		x := os.Getenv(v)
		if x != "" {
			return x
		}
	}
	return ""
}

// TweetLoader abstracts functions in the Twitter API that can retrieve tweets.
type TweetLoader func(url.Values) ([]anaconda.Tweet, error)

// TweetFilter contains constraints on which tweets should be loaded
type TweetFilter struct {
	MaxDate time.Time
}

func allowTweet(tweet anaconda.Tweet, filter TweetFilter) bool {
	dt, _ := time.Parse("Mon Jan 02 15:04:05 +0000 2006", tweet.CreatedAt)
	if dt.Before(filter.MaxDate) {
		return true
	}
	return false
}

func loadTweets(loader TweetLoader, filter TweetFilter, stream chan<- anaconda.Tweet, tweetType string) {

	var errorCount int
	var minID int64
	params := url.Values{}
	params.Set("screen_name", cfg.Auth.Username)
	params.Set("count", "200")
	params.Set("include_rts", "1")

	for {

		tweets, err := loader(params)

		if err != nil {
			fmt.Printf("Error retrieving %ss: %s\n", tweetType, err.Error())
			errorCount++
			if errorCount >= maxErrorCount {
				break
			}
			continue
		}

		if *debug {
			fmt.Printf("Retrieved %ss: %d %d\n", tweetType, len(tweets), minID)
		}

		if len(tweets) == 0 {
			break
		}

		errorCount = 0

		for _, tweet := range tweets {
			if minID == 0 || tweet.Id < minID {
				minID = tweet.Id
			}
			if allowTweet(tweet, filter) {
				stream <- tweet
			}
		}

		minID--
		params.Set("max_id", fmt.Sprintf("%d", minID))

	} // loop

	close(stream)

	if *debug {
		fmt.Printf("Exiting load %ss\n", tweetType)
	}

	latch.Done()

}

func removeTweets(stream <-chan anaconda.Tweet, tweetType string) {

	latch.Add(1)

	for tweet := range stream {
		dt, _ := time.Parse("Mon Jan 02 15:04:05 +0000 2006", tweet.CreatedAt)
		fmt.Printf("%s: %d %s - %s\n", tweetType, tweet.Id, dt.Local().Format("02.01.06 15:04:05"), tweet.Text)
		if tweetType == Tweet {
			if *xoxo {
				_, err := twitter.DeleteTweet(tweet.Id, false)
				if err != nil {
					fmt.Printf("Error deleting tweet: %s\n", err.Error())
				}
			}
		} else if tweetType == Like {
			if *xoxo {
				_, err := twitter.Unfavorite(tweet.Id)
				if err != nil {
					fmt.Printf("Error unliking tweet: %s\n", err.Error())
				}
			}
		} else {
			fmt.Printf("Unknown tweet type: %s\n", tweetType)
		}
	}

	if *debug {
		fmt.Printf("Exiting log %ss\n", tweetType)
	}

	latch.Done()

}

func main() {

	flag.Parse()
	if *debug {
		fmt.Printf("debug: %t, commit: %t\n", *debug, *xoxo)
	}

	if cfg = GetConfig(); cfg == nil {
		fmt.Println("Missing configuration file")
		return
	}

	// TODO: validate config

	anaconda.SetConsumerKey(cfg.Auth.ConsumerKey)
	anaconda.SetConsumerSecret(cfg.Auth.ConsumerSecret)
	twitter = anaconda.NewTwitterApi(cfg.Auth.AccessToken, cfg.Auth.AccessSecret)

	maxDays := cfg.Filter.BacklogDays
	if *backlog > 0 {
		maxDays = *backlog
	}
	filter := TweetFilter{
		MaxDate: time.Now().Add(time.Duration(maxDays) * -24 * time.Hour),
	}
	fmt.Printf("Filter: %d days, %s\n", maxDays, filter.MaxDate.Format("02.01.06 15:04:05"))

	var chTw = make(chan anaconda.Tweet)
	var chLk = make(chan anaconda.Tweet)

	latch.Add(2)
	go loadTweets(twitter.GetUserTimeline, filter, chTw, Tweet)
	go loadTweets(twitter.GetFavorites, filter, chLk, Like)
	go removeTweets(chTw, Tweet)
	go removeTweets(chLk, Like)
	latch.Wait()

}
