// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// A simple CLI application to watch the activity of a given set of stocks.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/nsf/termbox-go"
)

const TIMEOUT = time.Duration(time.Second * 10)                                   // how long to wait on a call
const URL = "http://finance.yahoo.com/webservice/v1/symbols/%s/quote?format=json" // where we're getting our data from
const UP = "↑"                                                                    // rune 8593
const DOWN = "↓"                                                                  // rune 8595

var re = regexp.MustCompile(`^\d.+\.\d{2}`) // this is to have only 2 decimal places
var signalChan = make(chan os.Signal, 1)    // channel to catch ctrl-c

// Flag variables to hold CLI arguments
var (
	symbolFlag   = flag.String("s", "", "Symbols for ticker, comma seperate (no spaces)")
	intervalFlag = flag.Int("i", 1, "Interval for stock data to be updated in seconds")
)

// Stock is the top level of the returned JSON
type Stock struct {
	List List `json:"list"`
}

// List hold the metadata and list of returned symbol data
type List struct {
	Meta      Meta        `json:"meta"`
	Resources []Resources `json:"resources"`
}

// Meta is the calls metadata
type Meta struct {
	Type  string `json:"type"`
	Start uint   `json:"start"`
	Count uint   `json:"count"`
}

// Resources holds a JSON obj with the symbol data
type Resources struct {
	Resource Resource `json:"resource"`
}

// Resource contains the actual JSON obj with the symbol data
type Resource struct {
	Classname string `json:"classname"`
	Fields    Fields `json:"fields"`
}

// Fields holds all of the retrieved data from the API call
type Fields struct {
	Name    string `json:"name"`    // name of company
	Price   string `json:"price"`   // current price
	Symbol  string `json:"symbol"`  // stock symbol
	TS      string `json:"ts"`      //
	Type    string `json:"type"`    // type of stock (equity, etc...)
	UTCTime string `json:"utctime"` // time in UTC
	Volume  string `json:"volume"`  // shares traded
}

// stockwatcher holds the relevant data for the running instance
type stockwatcher struct {
	quotes   map[string]map[string]float64
	interval time.Duration
	m        *sync.Mutex
}

// NewStockWatcher returns a new instance of stockwatcher with the given parameters
func NewStockWatcher(i time.Duration) *stockwatcher {
	return &stockwatcher{
		quotes:   make(map[string]map[string]float64),
		interval: i,
		m:        &sync.Mutex{},
	}
}

// add takes the given symbol and populates a key in the quotes map
func (t *stockwatcher) add(symbol string) {
	t.m.Lock()
	defer t.m.Unlock()
	if _, ok := t.quotes[symbol]; !ok {
		t.quotes[symbol] = map[string]float64{}
	}
}

// updateStock populates stockwatcher struct with stock prices
func (t *stockwatcher) updateStock(symbol string, price float64) {
	t.m.Lock()
	defer t.m.Unlock()
	t.quotes[symbol] = map[string]float64{
		"previous": t.quotes[symbol]["current"],
		"current":  price,
	}
}

// query will retrieve data for a given symbol
func query(symbol string) (*Stock, error) {
	data := &Stock{}
	client := http.Client{
		Timeout: TIMEOUT,
	}

	resp, err := client.Get(fmt.Sprintf(URL, symbol))
	if err != nil {
		return nil, errors.New("unable to retrive symbol data")
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Fatalln(err)
	}
	return data, nil
}

// convertPrice converts the given string to a float64 value
func convertPrice(p string) float64 {
	price, err := strconv.ParseFloat(p, 64)
	if err != nil {
		log.Fatalln(err)
		os.Exit(1)
	}
	return price
}

// runner goes through and gets the data for each symbol
func (t *stockwatcher) runner() {
	var wg sync.WaitGroup
	for k, _ := range t.quotes {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			stock, err := query(k)
			if err != nil { // if we can't get a response from the API, put 0.00's in and keep going
				t.updateStock(k, 0.00)
				return
			}
			t.updateStock(stock.List.Resources[0].Resource.Fields.Symbol,
				convertPrice(re.FindString(stock.List.Resources[0].Resource.Fields.Price)),
			)
		}(k)
	}
	wg.Wait()
}

// formatData formats the given data for printing
func (t *stockwatcher) formatData() {
	// populate list with keys from quote map
	var keys []string
	for k := range t.quotes {
		keys = append(keys, k)
	}
	sort.Strings(keys) // alphabetize keys

	pos := 1
	for _, k := range keys {
		// print format for first run or if not change detected from previous run
		if t.quotes[k]["previous"] == 0.00 || t.quotes[k]["previous"] == t.quotes[k]["current"] {
			printTb(1,
				pos,
				fmt.Sprintf("%-6s %-7v %11s %-4s\n", k, t.quotes[k]["current"], "%", "-"),
				termbox.ColorWhite, termbox.ColorDefault,
			)
			pos++
			// print format in green if current price being is greater than previous price
		} else if t.quotes[k]["current"] > t.quotes[k]["previous"] {
			printTb(1,
				pos,
				fmt.Sprintf("%-6s %-7v +%-.6f %% %-4s\n", k, t.quotes[k]["current"], t.quotes[k]["current"]/t.quotes[k]["previous"], UP),
				termbox.ColorGreen,
				termbox.ColorDefault,
			)
			pos++
			// print format in red if current price being is lesser than previous price
		} else {
			printTb(1,
				pos,
				fmt.Sprintf("%-6s %-7v -%-.6f %% %-4s\n", k, t.quotes[k]["current"], t.quotes[k]["current"]/t.quotes[k]["previous"], DOWN),
				termbox.ColorRed,
				termbox.ColorDefault,
			)
			pos++
		}
	}
}

// printTb prints the given data out to the screen
func printTb(x, y int, msg string, fg, bg termbox.Attribute) {
	for _, c := range []rune(msg) {
		termbox.SetCell(x, y, c, fg, bg)
		x += runewidth.RuneWidth(c)
	}
	termbox.Flush()
}

func main() {
	flag.Parse()

	// make sure we got what was expected from the CLI
	if flag.NFlag() != 2 || *symbolFlag == "" {
		flag.Usage()
		os.Exit(1)
	}

	t := NewStockWatcher(time.Duration(*intervalFlag) * time.Second)

	// check if more than one symbol has been given
	switch {
	case strings.Contains(*symbolFlag, ","):
		for _, a := range strings.Split(*symbolFlag, ",") {
			t.add(a)
		}
	default:
		t.add(*symbolFlag)
	}

	// initialize termbox
	err := termbox.Init()
	if err != nil {
		log.Fatal(err)
	}
	termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)

	event := make(chan termbox.Event)
	go func() {
		for {
			// Post events to channel
			event <- termbox.PollEvent()
		}
	}()

loop:
	for {
		t.runner()
		t.formatData()

		// Poll key event or timeout (maybe)
		select {
		case <-event:
			break loop
		case <-time.After(t.interval):
			continue loop
		}
	}
	close(event)
	time.Sleep(1 * time.Second)
	termbox.Close()
	os.Exit(0) // close out on a good note
}
