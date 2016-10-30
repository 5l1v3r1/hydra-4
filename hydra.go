// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option)
// any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU General
// Public License for more details.
//
// You should have received a copy of the GNU General Public License along
// with this program.  If not, see <http://www.gnu.org/licenses/>.

// Penetration testing tool.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	defaultUserAgent   = "Mozilla/5.0 (Hydra)"
	defaultContentType = "application/x-www-form-urlencoded"
)

var (
	loginsStr          = flag.String("l", "", "A login or logins separated by colons")
	loginsFrom         = flag.String("L", "", "Load logins from FILE")
	passwordsStr       = flag.String("p", "", "A password or passwords separated by colons")
	passwordsFrom      = flag.String("P", "", "Load passwords from FILE")
	colonSeparatedFrom = flag.String("C", "", `Load lines in the colon separated "login:pass" format from FILE`)
	firstOnly          = flag.Bool("f", false, "Exit when a login/password pair is found")
	invertedCondition  = flag.Bool("i", false, "A fulfilled condition means an attempt was successful")
	conditionIsRegexp  = flag.Bool("regex", false, "The condition is a regular expression")
	numTasks           = flag.Int("t", 16, "A number of tasks to run in parallel")
	verbose            = flag.Bool("v", false, "Be verbose (show the response from the HTTP server)")
	showAttempts       = flag.Bool("V", false, "Show login+password for each attempt")
	outputTo           = flag.String("o", "", "Write found login/password pairs to FILE instead of stdout")
	headersAdd         Headers
	headersReplace     Headers

	retryQueueLength = flag.Int("r", 1024, "Length of the retry queue")

	postURL    string
	host       string
	data       string
	condition  []byte
	rCondition *regexp.Regexp

	jobs  chan Job
	retry chan Job
	wg    sync.WaitGroup

	m   sync.Mutex
	out io.WriteCloser = os.Stdout

	proxyURL *url.URL
)

type Header struct {
	key   string
	value string
}

type Headers []Header

func (hs *Headers) Set(val string) error {
	if !strings.Contains(val, ":") {
		return errors.New("invalid header: " + val)
	}

	kv := strings.SplitN(val, ":", 2)
	h := Header{
		key:   kv[0],
		value: strings.TrimLeft(kv[1], " "),
	}
	*hs = append(*hs, h)

	return nil
}

func (hs *Headers) String() string {
	s := make([]string, 0, len(*hs))
	for _, h := range *hs {
		s = append(s, fmt.Sprintf("%s: %s", h.key, h.value))
	}
	return strings.Join(s, "\n")
}

type Job struct {
	user string
	pass string
}

func readlines(fn string) (lines []string) {
	f, err := os.Open(fn)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return
}

func safeExit() {
	m.Lock()
	err := out.Close()
	m.Unlock()
	if err != nil {
		log.Print(err)
	}

	os.Exit(0)
}

func worker(n int) {
	defer wg.Done()

	client := http.Client{}
	if proxyURL != nil {
		client.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}

	var job Job
	ok := true
loop:
	for {
		if ok {
			select {
			case job, ok = <-jobs:
				if !ok {
					continue loop
				}
			case job = <-retry:
			default:
				break loop
			}
		} else {
			select {
			case job = <-retry:
			default:
				break loop
			}
		}

		if *showAttempts {
			fmt.Fprintf(os.Stderr, "[ATTEMPT] target %s - login %q - pass %q [worker %d]\n", host, job.user, job.pass, n)
		}

		postData := strings.Replace(data, "^USER^", url.QueryEscape(job.user), -1)
		postData = strings.Replace(postData, "^PASS^", url.QueryEscape(job.pass), -1)
		req, _ := http.NewRequest("POST", postURL, strings.NewReader(postData))

		req.Header.Add("Host", host)
		req.Header.Add("User-Agent", defaultUserAgent)
		req.Header.Add("Content-Length", strconv.Itoa(len(postData)))
		req.Header.Add("Content-Type", defaultContentType)
		req.Header.Add("Connection", "Keep-Alive")

		for _, h := range headersAdd {
			req.Header.Add(h.key, h.value)
		}
		for _, h := range headersReplace {
			req.Header.Set(h.key, h.value)
		}

		client.Jar, _ = cookiejar.New(nil)

		resp, err := client.Do(req)
		if err != nil {
			log.Print(err)
			select {
			case retry <- job:
			default:
			}
			continue
		}

		if *verbose {
			resp.Header.Write(os.Stderr)
			os.Stderr.Write([]byte{'\n'})
		}

		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Print(err)
			select {
			case retry <- job:
			default:
			}
			continue
		}

		if *verbose {
			os.Stderr.Write(body)
		}

		var failed bool
		if rCondition != nil {
			failed = rCondition.Match(body)
		} else {
			failed = bytes.Contains(body, condition)
		}
		if *invertedCondition {
			failed = !failed
		}
		if failed {
			continue
		}

		m.Lock()
		_, err = fmt.Fprintf(out, "%s:%s\n", job.user, job.pass)
		m.Unlock()
		if err != nil {
			log.Print(err)
		}

		if *firstOnly {
			safeExit()
		}
	}
}

func main() {
	log.SetFlags(log.Lshortfile)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: hydra [options] URL post-data condition

Options:
  -l login   A login or logins separated by colons
  -L FILE    Load logins from FILE
  -p pass    A password or passwords separated by colons
  -P FILE    Load passwords from FILE
  -C FILE    Load lines in the colon separated "login:pass" format from FILE
  -h header  Add an HTTP header
  -H header  Replace an HTTP header
  -i         A fulfilled condition means an attempt was successful
  -regex     The condition is a regular expression
  -f         Exit when a login/password pair is found
  -t TASKS   A number of tasks to run in parallel (default: 16)
  -o FILE    Write found login/password pairs to FILE instead of stdout
  -v         Be verbose (show the response from the HTTP server)
  -V         Show login+password for each attempt
  -r         Length of the retry queue (default: 1024)

Use HYDRA_PROXY environment variable for proxy setup.
`)
	}

	flag.Var(&headersAdd, "h", "Add an HTTP header")
	flag.Var(&headersReplace, "H", "Replace an HTTP header")
	flag.Parse()
	if len(flag.Args()) != 3 {
		flag.Usage()
		os.Exit(1)
	}

	if *loginsStr != "" && *loginsFrom != "" {
		log.Fatal("both -l and -L are specified")
	}

	if *passwordsStr != "" && *passwordsFrom != "" {
		log.Fatal("both -p and -P are specified")
	}

	if *colonSeparatedFrom != "" &&
		(*loginsStr != "" ||
			*loginsFrom != "" ||
			*passwordsStr != "" ||
			*passwordsFrom != "") {
		log.Fatal("both -C and one of -l/-L/-p/-P are specified")
	}

	if *colonSeparatedFrom == "" {
		if *loginsStr == "" && *loginsFrom == "" {
			log.Fatal("no logins are specified")
		}
		if *passwordsStr == "" && *passwordsFrom == "" {
			log.Fatal("no passwords are specified")
		}
	}

	postURL = flag.Arg(0)
	parsed, err := url.Parse(postURL)
	if err != nil {
		log.Fatal("invalid URL: " + err.Error())
	}

	host = parsed.Host
	data = flag.Arg(1)
	if *conditionIsRegexp {
		rCondition = regexp.MustCompile(flag.Arg(2))
	} else {
		condition = []byte(flag.Arg(2))
	}

	proxy := os.Getenv("HYDRA_PROXY")
	if proxy != "" {
		proxyURL, err = url.Parse(proxy)
		if err != nil {
			log.Fatal("invalid proxy URL: " + err.Error())
		}
	}

	retry = make(chan Job, *retryQueueLength)
	jobs = make(chan Job, *numTasks)
	wg.Add(*numTasks)
	for i := 0; i < *numTasks; i++ {
		go worker(i)
	}

	if *outputTo != "" {
		out, err = os.OpenFile(*outputTo, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			log.Fatal(err)
		}
		defer out.Close()

		sig := make(chan os.Signal)
		signal.Notify(sig, os.Interrupt)
		signal.Notify(sig, syscall.SIGTERM)

		go func() {
			<-sig
			safeExit()
		}()
	}

	if *colonSeparatedFrom != "" {
		f, err := os.Open(*colonSeparatedFrom)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			lp := strings.SplitN(scanner.Text(), ":", 2)
			if len(lp) < 2 {
				continue
			}

			jobs <- Job{lp[0], lp[1]}
		}
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	} else {
		var logins, passwords []string

		if *loginsFrom != "" {
			logins = readlines(*loginsFrom)
		} else {
			logins = strings.Split(*loginsStr, ":")
		}

		if *passwordsFrom != "" {
			passwords = readlines(*passwordsFrom)
		} else {
			passwords = strings.Split(*passwordsStr, ":")
		}

		for _, pass := range passwords {
			for _, user := range logins {
				jobs <- Job{user, pass}
			}
		}
	}

	close(jobs)
	wg.Wait()
}
