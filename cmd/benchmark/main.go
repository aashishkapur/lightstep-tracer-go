package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"runtime"
	"sync"
	"time"

	bench "github.com/lightstep/lightstep-benchmarks/benchlib"

	ls "github.com/lightstep/lightstep-tracer-go"
	bt "github.com/opentracing/basictracer-go"
	ot "github.com/opentracing/opentracing-go"
)

const (
	clientName = "golang"
)

var (
	logPayloadStr string
)

func fatal(x ...interface{}) {
	panic(fmt.Sprintln(x...))
}

func init() {
	lps := make([]byte, bench.LogsSizeMax)
	for i := 0; i < len(lps); i++ {
		lps[i] = 'A' + byte(i%26)
	}
	logPayloadStr = string(lps)
}

type testClient struct {
	baseURL string
	tracer  ot.Tracer
}

func work(n int64) int64 {
	const primeWork = 982451653
	x := int64(primeWork)
	for n != 0 {
		x *= primeWork
		n--
	}
	return x
}

func (t *testClient) getURL(path string) []byte {
	resp, err := http.Get(t.baseURL + path)
	if err != nil {
		fatal("Bench control request failed: ", err)
	}
	if resp.StatusCode != 200 {
		fatal("Bench control status != 200: ", resp.Status, ": ", path)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fatal("Bench error reading body: ", err)
	}
	return body
}

func (t *testClient) loop() {
	for {
		body := t.getURL(bench.ControlPath)

		control := bench.Control{}
		if err := json.Unmarshal(body, &control); err != nil {
			fatal("Bench control parse error: ", err)
		}
		if control.Exit {
			return
		}
		timing, flusht, sleeps, answer := t.run(&control)
		t.getURL(fmt.Sprintf(
			"%s?timing=%.9f&flush=%.9f&s=%.9f&a=%d",
			bench.ResultPath,
			timing.Seconds(),
			flusht.Seconds(),
			sleeps.Seconds(),
			answer))
	}
}

func testBody(control *bench.Control) (time.Duration, int64) {
	var sleep_debt time.Duration
	var answer int64
	var totalSleep time.Duration
	for i := int64(0); i < control.Repeat; i++ {
		span := ot.StartSpan("span/test")
		answer = work(control.Work)
		for i := int64(0); i < control.NumLogs; i++ {
			span.LogEventWithPayload("testlog",
				logPayloadStr[0:control.BytesPerLog])
		}
		span.Finish()
		sleep_debt += control.Sleep
		if sleep_debt <= control.SleepInterval {
			continue
		}
		begin := time.Now()
		time.Sleep(sleep_debt)
		elapsed := time.Now().Sub(begin)
		sleep_debt -= elapsed
		totalSleep += elapsed
	}
	return totalSleep, answer
}

func (t *testClient) run(control *bench.Control) (time.Duration, time.Duration, time.Duration, int64) {
	if control.Trace {
		ot.InitGlobalTracer(t.tracer)
	} else {
		ot.InitGlobalTracer(ot.NoopTracer{})
	}
	conc := control.Concurrent
	runtime.GOMAXPROCS(conc)
	runtime.GC()
	runtime.Gosched()

	var sleeps time.Duration
	var answer int64

	beginTest := time.Now()
	if conc == 1 {
		s, a := testBody(control)
		sleeps += s
		answer += a
	} else {
		start := &sync.WaitGroup{}
		finish := &sync.WaitGroup{}
		start.Add(conc)
		finish.Add(conc)
		for c := 0; c < conc; c++ {
			go func() {
				start.Done()
				start.Wait()
				s, a := testBody(control)
				sleeps += s
				answer += a
				finish.Done()
			}()
		}
		finish.Wait()
	}
	endTime := time.Now()
	flushDur := time.Duration(0)
	if control.Trace {
		recorder := t.tracer.(bt.Tracer).Options().Recorder.(*ls.Recorder)
		recorder.Flush()
		flushDur = time.Now().Sub(endTime)
	}
	return endTime.Sub(beginTest), flushDur, sleeps, answer
}

func main() {
	flag.Parse()
	tc := &testClient{
		baseURL: fmt.Sprint("http://",
			bench.ControllerHost, ":",
			bench.ControllerPort),
		tracer: ls.NewTracer(ls.Options{
			AccessToken: bench.ControllerAccessToken,
			Collector: ls.Endpoint{
				Host:      bench.ControllerHost,
				Port:      bench.GrpcPort,
				Plaintext: true,
			},
			// Verbose: true,
			UseGRPC: true,
		}),
	}
	tc.loop()
}