package main

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/transceptor-technology/go-siridb-connector"
	"github.com/transceptor-technology/goleri"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/nsf/termbox-go"
)

// AppVersion exposes version information
const AppVersion = "2.1.0"

var (
	xApp      = kingpin.New("siridb-admin", "Tool for communicating with a SiriDB database.")
	xDbname   = xApp.Flag("dbname", "Database name.").Short('d').Required().String()
	xServers  = xApp.Flag("servers", "Server(s) to connect to. Multiple servers are allowed and should be separated with a comma. (syntax: --servers=host[:port]").Short('s').Required().String()
	xUser     = xApp.Flag("user", "Database user.").Short('u').Required().String()
	xPassword = xApp.Flag("password", "Password for the database user.").Short('p').String()
	xHistory  = xApp.Flag("history", "Number of command in history. A value of 0 disables history.").Default("1000").Uint16()
	xTimeout  = xApp.Flag("timeout", "Query timeout in seconds.").Default("60").Uint16()
	xJSON     = xApp.Flag("json", "Raw JSON output.").Bool()
	xVersion  = xApp.Flag("version", "Print version information and exit.").Short('v').Bool()
)

func tbprint(x, y int, fg, bg termbox.Attribute, msg string) {
	for _, c := range msg {
		termbox.SetCell(x, y, c, fg, bg)
		x += runewidth.RuneWidth(c)
	}
}

const coldef = termbox.ColorDefault
const cViewLog = 0
const cViewOutput = 1

var logger = newLogView()
var outv = newView()
var client *siridb.Client
var currentView = cViewLog
var outPrompt = newPrompt(">>> ", coldef|termbox.AttrBold, coldef)
var his *history
var siriGrammar = SiriGrammar()

func logHandle(logCh chan string) {
	for {
		msg := <-logCh
		logger.append(msg)
		draw()
	}
}

func drawPassPrompt(p *prompt) {
	termbox.Clear(coldef, coldef)
	w, h := termbox.Size()
	p.draw(0, 0, w, h, coldef, coldef)
	termbox.Flush()
}

func draw() {
	termbox.Clear(coldef, coldef)
	w, h := termbox.Size()
	x := 0

	var s, tmp string
	var fg termbox.Attribute

	if currentView == cViewLog {
		s = " Log (ESC / CTRL+L close log, CTRL+Q quit)"
	} else {
		s = " Output (CTRL+L view log, CTRL+J copy to clipboard, CTRL+Q quit)"
	}
	for _, c := range s {
		termbox.SetCell(x, 0, c, termbox.ColorBlack, termbox.ColorWhite)
		x++
	}
	s = fmt.Sprintf(" <%s@%s> status: ", *xUser, *xDbname)
	if client.IsAvailable() {
		tmp = "OK "
		fg = termbox.ColorGreen
	} else {
		tmp = "NO CONNECTION "
		fg = termbox.ColorRed
	}
	end := w - len(s) - len(tmp)
	for ; x < end; x++ {
		termbox.SetCell(x, 0, ' ', termbox.ColorBlack, termbox.ColorWhite)
	}

	for _, c := range s {
		termbox.SetCell(x, 0, c, termbox.ColorBlack, termbox.ColorWhite)
		x++
	}

	for _, c := range tmp {
		termbox.SetCell(x, 0, c, fg, termbox.ColorWhite)
		x++
	}

	if currentView == cViewLog {
		logger.draw(w, h)
	} else {
		outv.draw(w, h)
		outPrompt.draw(0, h-1, w, h, coldef, coldef)
	}

	termbox.Flush()
}

func sendCommand() int {
	s := strings.TrimSpace(string(outPrompt.text))
	if strings.Compare(s, "exit") == 0 {
		return 1
	}
	his.insert(s)
	q := newQuery(s)
	q.parse(*xTimeout)
	w, _ := termbox.Size()
	outv.append(q, w)
	outPrompt.deleteAllRunes()

	return 0
}

func toClipboard() {
	var err error
	var s string

	if outv.query == nil {
		err = fmt.Errorf("nothing to copy")
	} else {
		s, err = outv.query.json()
		if err == nil {
			err = clipboard.WriteAll(s)
		}
	}
	if err == nil {
		logger.append(fmt.Sprintf("successfully copied last result to clipboard"))
	} else {
		logger.append(fmt.Sprintf("cannot copy to clipboard: %s", err.Error()))
	}
}

func getCompletions(p *prompt) []*completion {
	q := p.textBeforeCursor()
	res, err := siriGrammar.Parse(q)
	if err != nil {
		logger.append(fmt.Sprintf("goleri parse error: %s", err.Error()))
		return nil
	}

	var completions []*completion
	rest := q[res.Pos():]
	trimmed := strings.TrimSpace(q)
	if len(trimmed) < 4 && strings.HasPrefix("exit", trimmed) {
		compl := completion{
			text:     "exit",
			display:  "exit",
			startPos: len(trimmed),
		}
		completions = append(completions, &compl)
	}

	if strings.HasPrefix("import ", trimmed) {
		if len(trimmed) >= 6 {
			p := strings.TrimSpace(trimmed[6:])
			if len(p) == 0 {
				p = "."
			}
			if files, err := ioutil.ReadDir(p); err == nil {
				for _, f := range files {
					compl := completion{
						text:     fmt.Sprintf("%s ", f.Name()),
						display:  f.Name(),
						startPos: len(rest),
					}
					completions = append(completions, &compl)
				}
			}
		} else {
			compl := completion{
				text:     "import ",
				display:  "import",
				startPos: len(trimmed),
			}
			completions = append(completions, &compl)
		}
	}

	for _, elem := range res.GetExpecting() {
		if kw, ok := elem.(*goleri.Keyword); ok {
			word := kw.GetKeyword()
			if len(p.text) == 0 || len(rest) == 0 && len(q) > 0 && q[len(q)-1] == ' ' || len(rest) > 0 && strings.HasPrefix(word, rest) {
				compl := completion{
					text:     fmt.Sprintf("%s ", word),
					display:  word,
					startPos: len(rest),
				}
				completions = append(completions, &compl)
			}
		}
	}
	return completions
}

var timePrecision *string

func initConnect() {
	var tp string

	for !client.IsConnected() {
		time.Sleep(time.Second)
	}
	res, err := client.Query("show time_precision", 10)
	if err != nil {
		logger.append(fmt.Sprintf("error reading time_precision: %s", err.Error()))
		return
	}
	v, ok := res.(map[string]interface{})
	if !ok {
		logger.append("error reading time_precision: missing 'map' in data")
		return
	}

	arr, ok := v["data"].([]interface{})
	if !ok || len(arr) != 1 {
		logger.append("error reading time_precision: missing array 'data' or length 1 in map")
		return
	}

	tp, ok = arr[0].(map[string]interface{})["value"].(string)

	if !ok {
		logger.append("error reading time_precision: cannot find time_precision in data")
		return
	}

	logger.append(fmt.Sprintf("finished reading time precision: '%s'", tp))
	timePrecision = &tp
}

func main() {
	rand.Seed(time.Now().Unix())

	_, err := xApp.Parse(os.Args[1:])
	if err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}

	if *xVersion {
		fmt.Printf("Version: %s\n", AppVersion)
		os.Exit(0)
	}

	if *xJSON {
		outv.setModeJSON()
	}

	err = termbox.Init()
	if err != nil {
		panic(err)
	}
	defer termbox.Close()
	termbox.SetInputMode(termbox.InputEsc | termbox.InputMouse)
	termbox.SetOutputMode(termbox.Output256)

	logCh := make(chan string)

	go logHandle(logCh)

	var servers []server
	servers, err = getServers(*xServers)
	if err != nil {
		logger.append(fmt.Sprintf("error reading servers: %s", err))
	}

	if len(*xPassword) == 0 {
		pp := newPrompt("Password: ", coldef|termbox.AttrBold, coldef)
		pp.hideText = true

	passloop:
		for {
			drawPassPrompt(pp)
			switch ev := termbox.PollEvent(); ev.Type {
			case termbox.EventKey:
				switch ev.Key {
				case termbox.KeyCtrlC, termbox.KeyCtrlQ:
					termbox.Close()
					os.Exit(1)
				case termbox.KeyEnter:
					*xPassword = string(pp.text)
					break passloop
				default:
					pp.parse(ev)
				}
			case termbox.EventError:
				panic(ev.Err)
			}

		}
	}

	var historyFnP *string
	historyFn, err := homedir.Dir()
	if err == nil {
		historyFn = path.Join(historyFn, ".siridb-prompt", fmt.Sprintf("%s@%s.history.1", *xUser, *xDbname))
		historyFnP = &historyFn
	}

	his = newHistory(int(*xHistory), historyFnP)
	his.load()
	defer his.save()

	client = siridb.NewClient(
		*xUser,                      // user
		*xPassword,                  // password
		*xDbname,                    // database
		serversToInterface(servers), // siridb server(s)
		logCh, // optional log channel
	)

	client.Connect()
	go initConnect()
	if client.IsAvailable() {
		currentView = cViewOutput
	}
	outPrompt.completer = getCompletions

	defer client.Close()

	draw()

mainloop:
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			if currentView == cViewLog {
				switch ev.Key {
				case termbox.KeyCtrlQ:
					break mainloop
				case termbox.KeyCtrlL, termbox.KeyEsc:
					currentView = cViewOutput
				case termbox.KeyArrowUp:
					logger.up()
				case termbox.KeyArrowDown:
					logger.down()
				case termbox.KeyPgdn:
					logger.pageDown()
				case termbox.KeyPgup:
					logger.pageUp()
				}
			} else if currentView == cViewOutput {
				switch ev.Key {
				case termbox.KeyCtrlQ:
					break mainloop
				case termbox.KeyCtrlL:
					currentView = cViewLog
				case termbox.KeyCtrlJ:
					toClipboard()
				case termbox.KeyEnter:
					outPrompt.clearCompletions()
					if sendCommand() == 1 {
						break mainloop
					}
				case termbox.KeyPgdn:
					outv.pageDown()
				case termbox.KeyPgup:
					outv.pageUp()
				case termbox.KeyArrowUp:
					if outPrompt.hasCompletions() {
						outPrompt.parse(ev)
					} else {
						outPrompt.setText(his.prev())
						outPrompt.clearCompletions()
					}
				case termbox.KeyArrowDown:
					if outPrompt.hasCompletions() {
						outPrompt.parse(ev)
					} else {
						outPrompt.setText(his.next())
						outPrompt.clearCompletions()
					}
				default:
					outPrompt.parse(ev)
				}
			}
		case termbox.EventMouse:
			if currentView == cViewLog {
				switch ev.Key {
				case termbox.MouseWheelUp:
					logger.up()
				case termbox.MouseWheelDown:
					logger.down()
				}
			} else if currentView == cViewOutput {
				switch ev.Key {
				case termbox.MouseWheelUp:
					outv.up()
				case termbox.MouseWheelDown:
					outv.down()
				}
			}
		case termbox.EventError:
			panic(ev.Err)
		}
		draw()
	}
}
