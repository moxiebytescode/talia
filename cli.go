// Package talia provides the core logic for checking domain availability via
// WHOIS and processing JSON domain lists.
package talia

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

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
		out, err := json.MarshalIndent(results, "", "  ")
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
		groupedData := ConvertArrayToGrouped(results)

		if outputFile == "" {
			data := ExtendedGroupedData{Available: groupedData.Available, Unavailable: groupedData.Unavailable}
			if err := writeGroupedJSON(inputPath, data); err != nil {
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

	// Extract domain names for checking
	domainNames := make([]string, len(ext.Unverified))
	for i := range ext.Unverified {
		domainNames[i] = ext.Unverified[i].Domain
	}

	results := checkDomains(domainNames, whoisServer, sleep, verbose, workers)

	newest := ConvertArrayToGrouped(results)

	if groupedOutput && outputFile != "" {
		// Only fresh checks may replace results already in the output file.
		if err := WriteGroupedFile(finalOutputFile, newest); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing grouped JSON to %s: %v\n", finalOutputFile, err)
			return 1
		}
	} else {
		ext = applyGroupedChecks(ext, newest)
		if err := writeGroupedJSON(finalOutputFile, ext); err != nil {
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
		if trimmed := bytes.TrimSpace(raw); json.Valid(raw) || (len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')) {
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
				if file, err := readDomainFile(targetFile, true); err == nil {
					hasUnverified = len(file.asGrouped().Unverified) > 0
				}
				if !hasUnverified {
					suggestCount = n
				}
			}
		}
	}

	if suggestCount > 0 {
		file, err := readDomainFile(targetFile, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading domain file:", err)
			return 1
		}
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
			existingDomains = file.existingDomains()
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

		allResults, firstErr := generateSuggestionBatch(os.Getenv("OPENAI_API_KEY"), promptText, suggestCount, modelName, baseURL, existingDomains, parallelReqs)

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
			file, err := readDomainFile(inputPath, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", inputPath, err)
				return 1
			}
			ext := file.asGrouped()
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

	file, err := decodeDomainFile(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing JSON in %s: %v\n", inputPath, err)
		return 1
	}
	if file.isArray {
		return RunCLIDomainArray(*whoisServer, inputPath, file.array, *sleep, *verbose, *groupedOutput, *outputFile, workers)
	}
	return RunCLIGroupedInput(*whoisServer, inputPath, file.grouped, *sleep, *verbose, *groupedOutput, *outputFile, workers)
}
