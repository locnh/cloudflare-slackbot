package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cloudflare/cloudflare-go"
	"github.com/ian-kent/gofigure"
	"github.com/nlopes/slack"
)

// support , separated env vars for URL_BASES and URL_SUFFIXES
var _ = os.Setenv("GOFIGURE_ENV_ARRAY", "1")

type config struct {
	CloudflareToken    string   `env:"CF_TOKEN"`
	CloudflareEmail    string   `env:"CF_EMAIL"`
	CloudflareZone     string   `env:"CF_ZONE"`
	SlackToken         string   `env:"SLACK_TOKEN"`
	RestrictedChannels []string `env:"RESTRICTED_CHANNELS"`
	AuthorisedUsers    []string `env:"AUTHORISED_USERS"`
}

type cacheClearInput struct {
	URI string `json:"uri,omitempty"`
}

type cacheClearPending struct {
	Everything bool
	URIs       []string
	Created    time.Time
	User       string
	Channel    string
}

var clearPending = make(map[string]cacheClearPending)
var clearWaiting []cacheClearPending

var helpMessage = "Here's some examples of how to clear the cache:\n`clear cache`\n`clear cache url1 url2 url3`\nIf I ask you to confirm, reply with `yes` or `no`!"

var cacheQueue = make(chan cacheClearPending, 10)
var wg sync.WaitGroup
var cfg = config{}

var cfZoneID = ""

var api *slack.Client
var botUserID string
var restrictedChannels []string
var authorisedUsers []string

func main() {
	err := gofigure.Gofigure(&cfg)
	if err != nil {
		panic(err)
	}

	wg.Add(2)
	go slackBot()
	go cacheDeleter()
	wg.Wait()
}

func cacheDeleter() {
	defer wg.Done()

	t := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-t.C:
			fmt.Println("tick")
			if len(clearWaiting) > 0 {
				fmt.Println("found items in waiting")
				for _, v := range clearWaiting {
					err := v.do()
					if err != nil {
						log.Printf("cacheDeleter: Error received: %s\n", err.Error())
						api.PostMessage(v.Channel, "<@"+v.User+"> Sorry, that didn't work...\n*Error*: "+err.Error(), slack.PostMessageParameters{AsUser: true})
						continue
					}
					log.Println("cacheDeleter: 'do' completed without errors")
					if len(v.URIs) > 0 {
						f := strings.Join(v.URIs, "`\n`")
						f = "`" + f + "`"
						api.PostMessage(v.Channel, "<@"+v.User+"> That's done, the following items have been cleared:\n"+f, slack.PostMessageParameters{AsUser: true})
					} else {
						api.PostMessage(v.Channel, "<@"+v.User+"> That's done, the entire cache has been cleared", slack.PostMessageParameters{AsUser: true})
					}
				}
				clearWaiting = make([]cacheClearPending, 0)
			}
		case q := <-cacheQueue:
			fmt.Println("adding item to clearWaiting queue")
			clearWaiting = append(clearWaiting, q)
		}
	}
}

func (c cacheClearPending) do() error {
	api, _ := cloudflare.New(cfg.CloudflareToken, cfg.CloudflareEmail)

	if cfZoneID == "" {
		cfZoneID, _ = api.ZoneIDByName(cfg.CloudflareZone)
		fmt.Println(cfZoneID)
	}

	if c.Everything {
		log.Println("cacheClearPending [do]: Clearing everything")
		res, err := api.PurgeEverything(cfZoneID)
		fmt.Printf("CF Response: %+v", res)
		if err != nil {
			log.Printf("CF Error: %+v\n", err.Error())
		}
	} else {
		log.Printf("cacheClearPending [do]: Clearing %d files\n", len(c.URIs))
		pcr := cloudflare.PurgeCacheRequest{
			Everything: false,
			Files:      c.URIs,
		}

		fmt.Printf("%+v\n", pcr)

		res, err := api.PurgeCache(cfZoneID, pcr)
		fmt.Printf("CF Response: %+v", res)
		if err != nil {
			log.Printf("CF Error: %+v\n", err.Error())
		}
	}

	log.Println("cacheClearPending [do]: Completed without errors")

	return nil
}

func slackBot() {
	defer wg.Done()
	api = slack.New(cfg.SlackToken)

	a, err := api.AuthTest()
	if err != nil {
		panic(err)
	}

	c, err := api.GetChannels(true)
	if err != nil {
		panic(err)
	}

	botUserID = a.UserID

	rtm := api.NewRTM()
	go rtm.ManageConnection()

	for _, channel := range c {
		api.PostMessage(channel.ID, "I'm ready! Say `help` for more information.", slack.PostMessageParameters{AsUser: true})
		for _, r := range cfg.RestrictedChannels {
			if channel.Name == r {
				restrictedChannels = append(restrictedChannels, channel.ID)
			}
		}
	}

	u, err := api.GetUsers()
	for _, user := range u {
		for _, a := range cfg.AuthorisedUsers {
			if user.Name == a {
				authorisedUsers = append(authorisedUsers, user.ID)
			}
		}
	}

Loop:
	for {
		select {
		case msg := <-rtm.IncomingEvents:
			//fmt.Print("Event Received: ")
			switch ev := msg.Data.(type) {
			case *slack.HelloEvent:
				// Ignore hello
			case *slack.ConnectedEvent:
				// fmt.Println("Infos:", ev.Info)
				// fmt.Println("Connection counter:", ev.ConnectionCount)

			case *slack.MessageEvent:
				// ignore cachebot user
				if ev.User == botUserID {
					continue
				}

				fmt.Printf("Message: %+v\n", ev)

				var channelIsAllowed bool
				for _, r := range restrictedChannels {
					if r == ev.Channel {
						channelIsAllowed = true
						break
					}
				}

				var authorised bool
				for _, a := range authorisedUsers {
					if a == ev.User {
						authorised = true
						break
					}
				}

				if !channelIsAllowed || !authorised {
					api.PostMessage(ev.Channel, "<@"+ev.User+"> Sorry, I'm not allowed to talk to you here :thinking_face:", slack.PostMessageParameters{AsUser: true})
					continue
				}

				switch strings.ToLower(ev.Text) {
				case "help":
					api.PostMessage(ev.Channel, "<@"+ev.User+"> "+helpMessage, slack.PostMessageParameters{AsUser: true})
					continue
				case "yes":
					if _, ok := clearPending[ev.User]; ok {
						api.PostMessage(ev.Channel, "<@"+ev.User+"> Ok, I'll let you know when it's done.", slack.PostMessageParameters{AsUser: true})
						cacheQueue <- clearPending[ev.User]
						delete(clearPending, ev.User)
					}
					continue
				case "no":
					if _, ok := clearPending[ev.User]; ok {
						api.PostMessage(ev.Channel, "<@"+ev.User+"> Ok, I'll cancel that!", slack.PostMessageParameters{AsUser: true})
						delete(clearPending, ev.User)
					}
					continue
				}

				re := regexp.MustCompile("https?:\\/\\/[a-z0-9./]+")
				if strings.Contains(ev.Text, "clear cache") {
					m := re.FindAllStringSubmatch(ev.Text, -1)
					fmt.Printf("Matches: %+v\n", m)

					if len(m) == 0 {
						api.PostMessage(ev.Channel, "<@"+ev.User+"> I'm about to clear the entire cache, are you sure?\n*Warning*: This will cause a spike in traffic to the production environment!", slack.PostMessageParameters{AsUser: true})
						clearPending[ev.User] = cacheClearPending{Everything: true, Created: time.Now(), User: ev.User, Channel: ev.Channel}
						continue
					}

					var uris []string
					for _, uri := range m {
						fmt.Printf("Clearing cache: %s\n", uri)
						uris = append(uris, uri...)
					}

					if len(uris) > 30 {
						api.PostMessage(ev.Channel, "<@"+ev.User+"> That's too much for one request - try again with less URIs", slack.PostMessageParameters{AsUser: true})
						continue
					}

					f := strings.Join(uris, "`\n`")
					f = "`" + f + "`"
					api.PostMessage(ev.Channel, "<@"+ev.User+"> I'm about to clear the following cache items, are you sure?\n"+f, slack.PostMessageParameters{AsUser: true})
					clearPending[ev.User] = cacheClearPending{Everything: false, Created: time.Now(), URIs: uris, User: ev.User, Channel: ev.Channel}
					continue
				}

			case *slack.PresenceChangeEvent:
				fmt.Printf("Presence Change: %v\n", ev)

			case *slack.LatencyReport:
				fmt.Printf("Current latency: %v\n", ev.Value)

			case *slack.RTMError:
				fmt.Printf("Error: %s\n", ev.Error())

			case *slack.InvalidAuthEvent:
				fmt.Printf("Invalid credentials")
				break Loop

			default:

				// Ignore other events..
				//fmt.Printf("Unexpected: %v\n", msg.Data)
			}
		}
	}
}
