package talia

import (
	"fmt"
	"sync"
	"time"
)

// checkDomains owns check interpretation in both scheduling modes. Zero workers
// selects sequential pacing; a negative count selects one worker per domain.
func checkDomains(domains []string, whoisServer string, sleep time.Duration, verbose bool, workers int) []DomainRecord {
	results := make([]DomainRecord, len(domains))
	prog := newProgress(len(domains))
	stats := newCheckStats()
	check := func(index int) {
		domain := domains[index]
		avail, reason, logData, err := CheckDomainAvailability(domain, whoisServer)
		if err != nil {
			avail, reason = false, ReasonError
			logData = fmt.Sprintf("Error: %v", err)
		}
		prog.IncrementAndPrint(domain, avail, reason)
		stats.Record(avail, reason)
		if !verbose && reason != ReasonError {
			logData = ""
		}
		results[index] = DomainRecord{Domain: domain, Available: avail, Reason: reason, Log: logData}
	}

	if workers == 0 {
		for i := range domains {
			check(i)
			time.Sleep(sleep)
		}
	} else {
		if workers < 0 || workers > len(domains) {
			workers = len(domains)
		}
		jobs := make(chan int, len(domains))
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					check(index)
				}
			}()
		}
		for i := range domains {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}
	stats.PrintSummary()
	return results
}
