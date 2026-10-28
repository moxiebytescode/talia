// Package talia provides the core logic for checking domain availability via
// WHOIS and processing JSON domain lists.
package talia

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// checkResult holds the result of a single domain availability check.
type checkResult struct {
	Domain string
	Avail  bool
	Reason AvailabilityReason
	Log    string
}

// shouldIncludeLog determines whether to include the WHOIS log in output.
func shouldIncludeLog(verbose bool, reason AvailabilityReason) bool {
	return verbose || reason == ReasonError
}

// checkDomains performs WHOIS checks on a list of domains and returns the results.
// If workers > 0, it uses parallel processing with the specified number of workers.
// If workers == 0, it uses sequential processing with sleep between checks.
func checkDomains(domains []string, whoisServer string, sleep time.Duration, verbose bool, workers int) []checkResult {
	if workers > 0 {
		return checkDomainsParallel(domains, whoisServer, verbose, workers)
	}
	return checkDomainsSequential(domains, whoisServer, sleep, verbose)
}

// checkDomainsSequential performs WHOIS checks sequentially with sleep between checks.
func checkDomainsSequential(domains []string, whoisServer string, sleep time.Duration, verbose bool) []checkResult {
	results := make([]checkResult, 0, len(domains))
	prog := newProgress(len(domains))
	stats := newCheckStats()

	for _, domain := range domains {
		avail, reason, logData, err := CheckDomainAvailability(domain, whoisServer)
		if err != nil {
			avail = false
			reason = ReasonError
			logData = fmt.Sprintf("Error: %v", err)
		}

		prog.IncrementAndPrint(domain, avail, reason)
		stats.Record(avail, reason)

		log := ""
		if shouldIncludeLog(verbose, reason) {
			log = logData
		}

		results = append(results, checkResult{
			Domain: domain,
			Avail:  avail,
			Reason: reason,
			Log:    log,
		})

		time.Sleep(sleep)
	}

	stats.PrintSummary()
	return results
}

// checkDomainsParallel performs WHOIS checks using a worker pool.
func checkDomainsParallel(domains []string, whoisServer string, verbose bool, workers int) []checkResult {
	// workers == -1 means unlimited (one per domain)
	if workers < 0 || workers > len(domains) {
		workers = len(domains)
	}

	results := make([]checkResult, len(domains))
	prog := newProgress(len(domains))
	stats := newCheckStats()

	// Job represents a domain to check with its index
	type job struct {
		index  int
		domain string
	}

	jobs := make(chan job, len(domains))
	var wg sync.WaitGroup

	// Start workers
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				avail, reason, logData, err := CheckDomainAvailability(j.domain, whoisServer)
				if err != nil {
					avail = false
					reason = ReasonError
					logData = fmt.Sprintf("Error: %v", err)
				}

				prog.IncrementAndPrint(j.domain, avail, reason)
				stats.Record(avail, reason)

				log := ""
				if shouldIncludeLog(verbose, reason) {
					log = logData
				}

				results[j.index] = checkResult{
					Domain: j.domain,
					Avail:  avail,
					Reason: reason,
					Log:    log,
				}
			}
		}()
	}

	// Send jobs
	for i, domain := range domains {
		jobs <- job{index: i, domain: domain}
	}
	close(jobs)

	wg.Wait()
	stats.PrintSummary()
	return results
}

// RunCLIDomainArray handles the original array input logic (non-grouped or grouped output).
func RunCLIDomainArray(
	whoisServer, inputPath string,
	domains []DomainRecord,
	sleep time.Duration,
	verbose, groupedOutput bool,
	outputFile string,
	workers int,
) int {
	// Extract domain names for checking
	domainNames := make([]string, len(domains))
	for i := range domains {
		domainNames[i] = domains[i].Domain
	}

	results := checkDomains(domainNames, whoisServer, sleep, verbose, workers)

	if !groupedOutput {
		// =========== Non-Grouped Mode ===========
		for i, res := range results {
			domains[i].Available = res.Avail
			domains[i].Reason = res.Reason
			domains[i].Log = res.Log
		}

		out, err := json.MarshalIndent(domains, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
			return 1
		}
		if err := os.WriteFile(inputPath, out, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
			return 1
		}
		fmt.Println("Processing complete. Updated file:", inputPath)
	} else {
		// =========== Grouped Mode ===========
		groupedData := GroupedData{}
		for _, res := range results {
			gd := GroupedDomain{
				Domain: res.Domain,
				Reason: res.Reason,
				Log:    res.Log,
			}
			if res.Avail {
				groupedData.Available = append(groupedData.Available, gd)
			} else {
				groupedData.Unavailable = append(groupedData.Unavailable, gd)
			}
		}

		if outputFile == "" {
			mergedOut, err := json.MarshalIndent(groupedData, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error marshaling grouped JSON: %v\n", err)
				return 1
			}
			if err := os.WriteFile(inputPath, mergedOut, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing grouped JSON to %s: %v\n", inputPath, err)
				return 1
			}
			fmt.Println("Processing complete in grouped-output mode (overwrote input).")
		} else {
			if err := WriteGroupedFile(outputFile, groupedData); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing grouped file: %v\n", err)
				return 1
			}
			fmt.Println("Processing complete in grouped-output mode (wrote to separate file).")
		}
	}

	return 0
}

// RunCLIGroupedInput handles input that's already in the grouped JSON format with unverified domains
func RunCLIGroupedInput(
	whoisServer, inputPath string,
	ext ExtendedGroupedData,
	sleep time.Duration,
	verbose, groupedOutput bool,
	outputFile string,
	workers int,
) int {
	finalOutputFile := outputFile
	if !groupedOutput || outputFile == "" {
		finalOutputFile = inputPath
	}

	if ext.Available == nil {
		ext.Available = []GroupedDomain{}
	}
	if ext.Unavailable == nil {
		ext.Unavailable = []GroupedDomain{}
	}

	// Extract domain names for checking
	domainNames := make([]string, len(ext.Unverified))
	for i := range ext.Unverified {
		domainNames[i] = ext.Unverified[i].Domain
	}

	results := checkDomains(domainNames, whoisServer, sleep, verbose, workers)

	newest := GroupedData{}
	for _, res := range results {
		gd := GroupedDomain{
			Domain: res.Domain,
			Reason: res.Reason,
			Log:    res.Log,
		}
		if res.Avail {
			newest.Available = append(newest.Available, gd)
		} else {
			newest.Unavailable = append(newest.Unavailable, gd)
		}
	}

	if groupedOutput && outputFile != "" {
		// Only fresh checks may replace results already in the output file.
		if err := WriteGroupedFile(finalOutputFile, newest); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing grouped JSON to %s: %v\n", finalOutputFile, err)
			return 1
		}
	} else {
		ext.Available = append(ext.Available, newest.Available...)
		ext.Unavailable = append(ext.Unavailable, newest.Unavailable...)
		ext.Unverified = nil

		out, err := json.MarshalIndent(ext, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling grouped JSON: %v\n", err)
			return 1
		}
		if err := os.WriteFile(finalOutputFile, out, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing grouped JSON to %s: %v\n", finalOutputFile, err)
			return 1
		}
	}

	if finalOutputFile == inputPath {
		fmt.Println("Processed grouped input (with unverified) and overwrote original file.")
	} else {
		fmt.Println("Processed grouped input (with unverified) and wrote results to:", finalOutputFile)
	}

	return 0
}

// skipEnvFile is a test hook to skip loading .env files during tests.
var skipEnvFile bool

// RunCLI is the main entry point for Talia logic.
func RunCLI(args []string) int {
	// Load .env file from current directory (silently ignore if not found)
	if !skipEnvFile {
		_ = LoadEnvFile(".env")
	}

	fs := flag.NewFlagSet("talia", flag.ContinueOnError)
	whoisServer := fs.String("whois", "", "WHOIS server, e.g. whois.verisign-grs.com:43 (env: WHOIS_SERVER)")
	sleep := fs.Duration("sleep", 2*time.Second, "Time to sleep between domain checks (default 2s)")
	verbose := fs.Bool("verbose", false, "Include WHOIS log in 'log' field even for successful checks")
	groupedOutput := fs.Bool("grouped-output", false, "Enable grouped output (JSON object with 'available','unavailable')")
	outputFile := fs.String("output-file", "", "Path to grouped output file (if set, input file remains unmodified)")
	suggest := fs.Int("suggest", 0, "Number of domain suggestions to generate (env: TALIA_SUGGEST)")
	suggestParallel := fs.Int("suggest-parallel", 1, "Number of parallel suggestion requests to run (env: TALIA_SUGGEST_PARALLEL)")
	prompt := fs.String("prompt", "", "Optional prompt to influence domain suggestions (env: TALIA_PROMPT)")
	model := fs.String("model", defaultOpenAIModel, "OpenAI model to use for suggestions (env: TALIA_MODEL)")
	apiBase := fs.String("api-base", "", "Base URL for OpenAI-compatible API (env: OPENAI_API_BASE)")
	fresh := fs.Bool("fresh", false, "Don't pass existing domains to AI (allows duplicates, starts fresh)")
	clean := fs.Bool("clean", false, "Clean and normalize domains in the file (removes invalid domains)")
	noVerify := fs.Bool("no-verify", false, "Skip WHOIS verification after generating suggestions")
	merge := fs.Bool("merge", false, "Merge multiple domain files")
	output := fs.String("o", "", "Output file for merge (if not set, merges into first file)")
	exportAvailable := fs.String("export-available", "", "Export available domains to a text file")
	lightspeed := fs.String("lightspeed", "", "Parallel workers: number or 'max' (env: TALIA_LIGHTSPEED)")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error parsing flags:", err)
		return 1
	}

	// Env fallbacks for these flags apply only when the flag wasn't passed,
	// so an explicit value always wins, even when it equals the default.
	explicitFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	// Get target file from args or env var
	targetFile := ""
	if fs.NArg() >= 1 {
		targetFile = fs.Arg(0)
	} else if envFile := os.Getenv("TALIA_FILE"); envFile != "" {
		targetFile = envFile
	}
	if targetFile == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <json-file> (or set TALIA_FILE env var)\n", fs.Name())
		return 1
	}
	if *clean {
		// Auto-detect format: try JSON first, fall back to plain text
		raw, readErr := os.ReadFile(targetFile)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "Error reading file:", readErr)
			return 1
		}
		var removed []string
		var err error
		if json.Valid(raw) {
			removed, err = cleanSuggestionsFile(targetFile)
		} else {
			removed, err = cleanTextFile(targetFile)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error cleaning file:", err)
			return 1
		}
		if len(removed) > 0 {
			fmt.Printf("Removed %d invalid domains:\n", len(removed))
			for _, d := range removed {
				fmt.Printf("  - %s\n", d)
			}
		} else {
			fmt.Println("No invalid domains found.")
		}
		fmt.Println("Cleaned", targetFile)
		return 0
	}

	if *merge {
		// In merge mode, all positional args are input files
		inputFiles := fs.Args()
		if len(inputFiles) < 1 {
			fmt.Fprintln(os.Stderr, "Error: --merge requires at least one input file")
			return 1
		}
		if len(inputFiles) < 2 && *output == "" {
			fmt.Fprintln(os.Stderr, "Error: --merge requires at least 2 files, or use -o to specify output")
			return 1
		}

		outputFile := *output
		if outputFile == "" {
			// Merge all into first file
			outputFile = inputFiles[0]
		}

		added, err := mergeFiles(outputFile, inputFiles)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error merging files:", err)
			return 1
		}
		fmt.Printf("Merged %d domains into %s\n", added, outputFile)
		return 0
	}

	if *exportAvailable != "" {
		added, err := exportAvailableDomains(targetFile, *exportAvailable)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error exporting available domains:", err)
			return 1
		}
		fmt.Printf("Exported %d available domains to %s\n", added, *exportAvailable)
		return 0
	}

	// Parse lightspeed flag: "" = sequential, "max" = unlimited, number = worker count
	// Falls back to TALIA_LIGHTSPEED env var
	workers := 0
	ls, lsSource := *lightspeed, "--lightspeed"
	if ls == "" {
		ls, lsSource = os.Getenv("TALIA_LIGHTSPEED"), "TALIA_LIGHTSPEED"
	}
	if ls != "" {
		if ls == "max" {
			workers = -1 // sentinel for "use domain count"
		} else {
			n, err := strconv.Atoi(ls)
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "Error: invalid %s value %q: want \"max\" or a positive integer\n", lsSource, ls)
				return 1
			}
			workers = n
		}
	}

	// Determine suggest count: use flag if provided, otherwise check env var
	// But only use env var if file has no unverified domains to check
	suggestCount := *suggest
	if !explicitFlags["suggest"] {
		if envSuggest := os.Getenv("TALIA_SUGGEST"); envSuggest != "" {
			if n, err := strconv.Atoi(envSuggest); err == nil && n > 0 {
				// Check if file has unverified domains - if so, don't use env var
				hasUnverified := false
				if raw, err := os.ReadFile(targetFile); err == nil {
					var ext ExtendedGroupedData
					if json.Unmarshal(raw, &ext) == nil && len(ext.Unverified) > 0 {
						hasUnverified = true
					}
				}
				if !hasUnverified {
					suggestCount = n
				}
			}
		}
	}

	if suggestCount > 0 {
		baseURL := *apiBase
		if baseURL == "" {
			baseURL = os.Getenv("OPENAI_API_BASE")
		}
		if baseURL == "" {
			baseURL = defaultOpenAIBase
		}
		// Use env var if --prompt not provided
		promptText := *prompt
		if promptText == "" {
			promptText = os.Getenv("TALIA_PROMPT")
		}
		// Use env var if --model not provided
		modelName := *model
		if !explicitFlags["model"] {
			if envModel := os.Getenv("TALIA_MODEL"); envModel != "" {
				modelName = envModel
			}
		}
		// Read existing domains to avoid duplicates (unless --fresh is set)
		var existingDomains []string
		if !*fresh {
			existingDomains = readExistingDomains(targetFile)
		}

		parallelReqs := *suggestParallel
		if !explicitFlags["suggest-parallel"] {
			if envParallel := os.Getenv("TALIA_SUGGEST_PARALLEL"); envParallel != "" {
				if n, err := strconv.Atoi(envParallel); err == nil && n > 0 {
					parallelReqs = n
				}
			}
		}
		if parallelReqs < 1 {
			parallelReqs = 1
		}

		fmt.Printf("Starting %d parallel requests (each requesting %d suggestions)...\n", parallelReqs, suggestCount)

		apiKey := os.Getenv("OPENAI_API_KEY")
		var allResults []DomainRecord
		var resultsMu sync.Mutex
		var wg sync.WaitGroup
		var firstErr error
		var errMu sync.Mutex
		var completed int
		var completedMu sync.Mutex

		for i := range parallelReqs {
			wg.Add(1)
			go func(reqNum int) {
				defer wg.Done()
				list, err := GenerateDomainSuggestions(apiKey, promptText, suggestCount, modelName, baseURL, existingDomains)

				completedMu.Lock()
				completed++
				current := completed
				completedMu.Unlock()

				if err != nil {
					fmt.Printf("  [%d/%d] Request %d failed: %v\n", current, parallelReqs, reqNum+1, err)
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
				fmt.Printf("  [%d/%d] Request %d returned %d suggestions\n", current, parallelReqs, reqNum+1, len(list))
				resultsMu.Lock()
				allResults = append(allResults, list...)
				resultsMu.Unlock()
			}(i)
		}
		wg.Wait()

		if firstErr != nil && len(allResults) == 0 {
			fmt.Fprintln(os.Stderr, "Error generating suggestions:", firstErr)
			return 1
		}
		if firstErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: some requests failed: %v\n", firstErr)
		}

		if err := writeSuggestionsFile(targetFile, allResults); err != nil {
			fmt.Fprintln(os.Stderr, "Error writing suggestions file:", err)
			return 1
		}
		fmt.Printf("Collected %d suggestions total, wrote to %s (duplicates removed)\n", len(allResults), targetFile)

		// Auto-verify suggestions if --whois is provided (or env var) and --no-verify is not set
		whois := *whoisServer
		if whois == "" {
			whois = os.Getenv("WHOIS_SERVER")
		}
		if whois != "" && !*noVerify {
			fmt.Println("Verifying suggestions...")
			inputPath := targetFile
			raw, err := os.ReadFile(inputPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", inputPath, err)
				return 1
			}
			var ext ExtendedGroupedData
			if err := json.Unmarshal(raw, &ext); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", inputPath, err)
				return 1
			}
			// Use 100ms sleep for auto-verification (or lightspeed if set)
			verifySleep := 100 * time.Millisecond
			return RunCLIGroupedInput(whois, inputPath, ext, verifySleep, *verbose, true, "", workers)
		}
		return 0
	}

	// Use env var if --whois not provided
	if *whoisServer == "" {
		*whoisServer = os.Getenv("WHOIS_SERVER")
	}
	if *whoisServer == "" {
		fmt.Fprintln(os.Stderr, "Error: --whois=<server:port> is required (or set WHOIS_SERVER env var)")
		return 1
	}

	inputPath := targetFile
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", inputPath, err)
		return 1
	}

	// Attempt to parse input as a simple array of DomainRecord.
	var domains []DomainRecord
	err = json.Unmarshal(raw, &domains)
	if err == nil {
		// Plain slice of domain records
		return RunCLIDomainArray(*whoisServer, inputPath, domains, *sleep, *verbose, *groupedOutput, *outputFile, workers)
	}

	// If that fails, try to parse as a grouped JSON that might contain unverified.
	var ext ExtendedGroupedData
	if err2 := json.Unmarshal(raw, &ext); err2 == nil {
		return RunCLIGroupedInput(*whoisServer, inputPath, ext, *sleep, *verbose, *groupedOutput, *outputFile, workers)
	}

	// If both fail, then it's truly invalid JSON or an unexpected format.
	fmt.Fprintf(os.Stderr, "Error parsing JSON in %s: %v\n", inputPath, err)
	return 1
}
