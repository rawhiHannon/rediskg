package setup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	minRedisVersion    = "8.0.0"
	minFalkorDBVersion = 41800 // 4.18.0
	falkorDBSOName     = "falkordb.so"
	rediskgDir         = ".rediskg"
)

// Result holds the outcome of the setup process.
type Result struct {
	RedisVersion    string
	FalkorDBVersion int
	RedisAddr       string
	FalkorDBPath    string
	NeedsRestart    bool
}

// Run executes the full setup flow interactively.
func Run(redisAddr, falkordbPath string) (*Result, error) {
	result := &Result{RedisAddr: redisAddr}
	reader := bufio.NewReader(os.Stdin)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	baseDir := filepath.Join(homeDir, rediskgDir)
	libDir := filepath.Join(baseDir, "lib")

	// Ensure our directory exists
	if err := os.MkdirAll(libDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", libDir, err)
	}

	// --- Step 1: Check Redis ---
	fmt.Println("\n[1/4] Checking Redis...")

	redisVersion, err := checkRedisServer(redisAddr)
	if err != nil {
		fmt.Printf("  Redis not reachable at %s\n", redisAddr)
		fmt.Println("  Redis is required. Install options:")
		fmt.Println("    Ubuntu/Debian: sudo apt install redis-server")
		fmt.Println("    macOS:         brew install redis")
		fmt.Println("    From source:   rediskg setup --build-redis")
		return nil, fmt.Errorf("redis not available: %w", err)
	}

	result.RedisVersion = redisVersion
	fmt.Printf("  Found Redis %s ✓\n", redisVersion)

	if !versionAtLeast(redisVersion, minRedisVersion) {
		fmt.Printf("\n  Redis %s is too old. Need >= %s\n", redisVersion, minRedisVersion)

		if confirm(reader, "  Build and install Redis 8 from source?") {
			if err := buildRedisFromSource(); err != nil {
				return nil, fmt.Errorf("redis build failed: %w", err)
			}
			fmt.Println("  Redis 8 built successfully.")
			fmt.Println("  Run these commands to install (needs sudo):")
			fmt.Println("    sudo cp /tmp/redis-build/src/redis-server /usr/bin/redis-server")
			fmt.Println("    sudo cp /tmp/redis-build/src/redis-cli /usr/bin/redis-cli")
			fmt.Println("    sudo systemctl restart redis-server")
			result.NeedsRestart = true
		} else {
			return nil, fmt.Errorf("redis >= %s required", minRedisVersion)
		}
	}

	// --- Step 2: Check FalkorDB module ---
	fmt.Println("\n[2/4] Checking FalkorDB module...")

	falkorVersion, err := checkFalkorDB(redisAddr)
	if err != nil || falkorVersion == 0 {
		fmt.Println("  FalkorDB module not loaded.")

		// Check if .so exists locally
		soPath := resolveFalkorDBPath(falkordbPath, libDir)

		if soPath == "" {
			fmt.Println("  No falkordb.so found locally.")

			if confirm(reader, "  Download prebuilt falkordb.so from GitHub releases?") {
				soPath = filepath.Join(libDir, falkorDBSOName)
				if err := downloadFalkorDB(soPath); err != nil {
					fmt.Printf("  Download failed: %v\n", err)
					fmt.Println("  Alternative: build from source")
					fmt.Println("    git clone --recurse-submodules https://github.com/FalkorDB/FalkorDB.git")
					fmt.Println("    cd FalkorDB && make")
					fmt.Println("    Then run: rediskg setup --falkordb-path /path/to/falkordb.so")
					return nil, fmt.Errorf("falkordb.so not available")
				}
				fmt.Printf("  Downloaded to %s ✓\n", soPath)
			} else if confirm(reader, "  Build FalkorDB from source? (requires cmake, gcc, cargo)") {
				soPath, err = buildFalkorDBFromSource(libDir)
				if err != nil {
					return nil, fmt.Errorf("falkordb build failed: %w", err)
				}
			} else {
				return nil, fmt.Errorf("falkordb.so required")
			}
		} else {
			fmt.Printf("  Found falkordb.so at %s\n", soPath)
		}

		result.FalkorDBPath = soPath

		// Try to load the module
		fmt.Println("  Loading FalkorDB module into Redis...")
		if err := loadFalkorDBModule(redisAddr, soPath); err != nil {
			fmt.Printf("  Cannot load module dynamically: %v\n", err)
			fmt.Println("  You need to add this to your redis.conf and restart Redis:")
			fmt.Printf("    loadmodule %s\n", soPath)
			fmt.Println("  Then: sudo systemctl restart redis-server")
			result.NeedsRestart = true
		} else {
			fmt.Println("  FalkorDB module loaded ✓")
		}
	} else {
		fmt.Printf("  Found FalkorDB v%d.%d.%d ✓\n",
			falkorVersion/10000, (falkorVersion/100)%100, falkorVersion%100)

		if falkorVersion < minFalkorDBVersion {
			fmt.Printf("\n  FalkorDB version %d is below minimum %d\n", falkorVersion, minFalkorDBVersion)
			fmt.Println("  Consider updating: rebuild FalkorDB and replace the .so file")
		}
	}

	result.FalkorDBVersion = falkorVersion

	// --- Step 3: Configure LLM API Key ---
	fmt.Println("\n[3/4] Configuring LLM API key...")
	if err := configureAPIKey(reader, baseDir); err != nil {
		fmt.Printf("  Warning: %v\n", err)
	}

	// --- Step 4: Verify ---
	fmt.Println("\n[4/4] Verifying...")

	if !result.NeedsRestart {
		if err := verifyGraphOperations(redisAddr); err != nil {
			fmt.Printf("  Graph operations test failed: %v\n", err)
			return result, err
		}
		fmt.Println("  Graph operations working ✓")
	}

	// Print summary
	fmt.Println("\n" + strings.Repeat("─", 40))
	fmt.Printf("  Redis:    %s at %s\n", result.RedisVersion, result.RedisAddr)
	if result.FalkorDBVersion > 0 {
		fmt.Printf("  FalkorDB: v%d.%d.%d\n",
			result.FalkorDBVersion/10000, (result.FalkorDBVersion/100)%100, result.FalkorDBVersion%100)
	}
	if result.NeedsRestart {
		fmt.Println("\n  ⚠ Redis restart required. See instructions above.")
	} else {
		fmt.Println("\n  Ready to use!")
	}
	fmt.Println(strings.Repeat("─", 40))

	return result, nil
}

// checkRedisServer connects to Redis and returns its version.
func checkRedisServer(addr string) (string, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.Info(ctx, "server").Result()
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "redis_version:")), nil
		}
	}
	return "", fmt.Errorf("cannot determine redis version")
}

// checkFalkorDB checks if the FalkorDB module is loaded and returns its version.
func checkFalkorDB(addr string) (int, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.Do(ctx, "MODULE", "LIST").Result()
	if err != nil {
		// MODULE LIST might be disabled, try graph command
		_, graphErr := client.Do(ctx, "GRAPH.QUERY", "__probe__", "RETURN 1").Result()
		client.Do(ctx, "GRAPH.DELETE", "__probe__")
		if graphErr == nil {
			return 1, nil // loaded but can't determine version
		}
		return 0, graphErr
	}

	// Parse module list to find graph module version
	if modules, ok := result.([]interface{}); ok {
		for i := 0; i < len(modules); i++ {
			if mod, ok := modules[i].([]interface{}); ok {
				name := ""
				ver := 0
				for j := 0; j < len(mod)-1; j += 2 {
					key, _ := mod[j].(string)
					switch key {
					case "name":
						name, _ = mod[j+1].(string)
					case "ver":
						if v, ok := mod[j+1].(int64); ok {
							ver = int(v)
						}
					}
				}
				if name == "graph" {
					return ver, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("graph module not found")
}

// resolveFalkorDBPath checks custom path, then default location.
func resolveFalkorDBPath(customPath, libDir string) string {
	if customPath != "" {
		if _, err := os.Stat(customPath); err == nil {
			return customPath
		}
	}

	defaultPath := filepath.Join(libDir, falkorDBSOName)
	if _, err := os.Stat(defaultPath); err == nil {
		return defaultPath
	}

	return ""
}

// loadFalkorDBModule attempts to dynamically load the FalkorDB module.
func loadFalkorDBModule(addr, soPath string) error {
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := client.Do(ctx, "MODULE", "LOAD", soPath).Result()
	return err
}

// verifyGraphOperations runs a quick test to ensure FalkorDB works.
func verifyGraphOperations(addr string) error {
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.Do(ctx, "GRAPH.QUERY", "__rediskg_verify__",
		"CREATE (n:_Test {v:1}) RETURN n.v").Result()
	if err != nil {
		return err
	}

	client.Do(ctx, "GRAPH.DELETE", "__rediskg_verify__")
	return nil
}

// downloadFalkorDB downloads a prebuilt falkordb.so from GitHub releases.
func downloadFalkorDB(destPath string) error {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	} else if arch == "arm64" {
		arch = "arm64v8"
	}
	osName := runtime.GOOS

	// This URL pattern should match our CI release structure
	url := fmt.Sprintf(
		"https://github.com/rawhi/rediskg/releases/latest/download/falkordb-%s-%s.so",
		osName, arch,
	)

	fmt.Printf("  Downloading from %s...\n", url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d (prebuilt binary may not be available yet)", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(destPath)
		return err
	}

	fmt.Printf("  Downloaded %d MB\n", written/1024/1024)
	return os.Chmod(destPath, 0755)
}

// buildRedisFromSource downloads and compiles Redis 8.
func buildRedisFromSource() error {
	fmt.Println("  Downloading Redis source...")

	buildDir := "/tmp/redis-build"
	os.RemoveAll(buildDir)

	// Download
	url := "https://github.com/redis/redis/archive/refs/tags/8.6.3.tar.gz"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	// Extract
	fmt.Println("  Extracting...")
	if err := extractTarGz(resp.Body, "/tmp"); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Rename to consistent dir
	os.Rename("/tmp/redis-8.6.3", buildDir)

	// Build
	fmt.Println("  Building Redis (this may take a few minutes)...")
	cmd := exec.Command("make", "-j", fmt.Sprintf("%d", runtime.NumCPU()))
	cmd.Dir = buildDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// buildFalkorDBFromSource clones and builds FalkorDB.
func buildFalkorDBFromSource(libDir string) (string, error) {
	buildDir := "/tmp/falkordb-build"
	os.RemoveAll(buildDir)

	fmt.Println("  Cloning FalkorDB (this will take a while)...")
	cmd := exec.Command("git", "clone", "--recurse-submodules", "-j8",
		"--branch", "v4.18.7", "--depth", "1",
		"https://github.com/FalkorDB/FalkorDB.git", buildDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	fmt.Println("  Building FalkorDB (this may take 5-10 minutes)...")
	cmd = exec.Command("make", "-j", fmt.Sprintf("%d", runtime.NumCPU()))
	cmd.Dir = buildDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	// Find the .so file
	soSrc := filepath.Join(buildDir, "bin", "linux-x64-release", "falkordb.so")
	if _, err := os.Stat(soSrc); err != nil {
		// Try alternate path
		soSrc = filepath.Join(buildDir, "bin", "linux-x64-release", "src", "falkordb.so")
		if _, err := os.Stat(soSrc); err != nil {
			return "", fmt.Errorf("cannot find built falkordb.so")
		}
	}

	// Copy to our lib dir
	soDest := filepath.Join(libDir, falkorDBSOName)
	if err := copyFile(soSrc, soDest); err != nil {
		return "", err
	}

	fmt.Printf("  Built and saved to %s\n", soDest)
	return soDest, nil
}

// configureAPIKey checks for an existing API key and prompts the user to set one if missing.
func configureAPIKey(reader *bufio.Reader, baseDir string) error {
	envFile := filepath.Join(baseDir, "..", "..", ".env") // try CWD first
	// Check if key already exists in environment
	for _, key := range []string{"OPENAI_API_KEY", "GPT_API_KEY"} {
		if os.Getenv(key) != "" {
			fmt.Printf("  Found %s in environment ✓\n", key)
			return nil
		}
	}

	// Check if .env exists in current directory
	cwd, _ := os.Getwd()
	envFile = filepath.Join(cwd, ".env")
	if data, err := os.ReadFile(envFile); err == nil {
		content := string(data)
		if strings.Contains(content, "OPENAI_API_KEY") || strings.Contains(content, "GPT_API_KEY") {
			fmt.Printf("  Found API key in %s ✓\n", envFile)
			return nil
		}
	}

	fmt.Println("  No LLM API key found.")
	fmt.Println("  Supported providers: OpenAI, Ollama (local, no key needed)")
	fmt.Println()

	if confirm(reader, "  Do you want to configure an OpenAI API key now?") {
		fmt.Print("  Enter your OpenAI API key: ")
		key, _ := reader.ReadString('\n')
		key = strings.TrimSpace(key)
		if key == "" {
			fmt.Println("  Skipped. You can set OPENAI_API_KEY later or use --api-key flag.")
			return nil
		}

		// Write to .env in current directory
		f, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("cannot write .env: %w", err)
		}
		defer f.Close()

		if _, err := fmt.Fprintf(f, "OPENAI_API_KEY=%s\n", key); err != nil {
			return fmt.Errorf("cannot write key to .env: %w", err)
		}

		fmt.Printf("  API key saved to %s ✓\n", envFile)
		fmt.Println("  Make sure .env is in your .gitignore!")
	} else {
		fmt.Println("  Skipped. Use --api-key flag, set OPENAI_API_KEY env var, or use Ollama (--llm ollama).")
	}

	return nil
}

// --- Helpers ---

func confirm(reader *bufio.Reader, prompt string) bool {
	fmt.Printf("%s [y/n] ", prompt)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

func versionAtLeast(current, minimum string) bool {
	cur := parseVersion(current)
	min := parseVersion(minimum)
	for i := 0; i < 3; i++ {
		if cur[i] > min[i] {
			return true
		}
		if cur[i] < min[i] {
			return false
		}
	}
	return true
}

func parseVersion(v string) [3]int {
	var parts [3]int
	segments := strings.Split(v, ".")
	for i := 0; i < len(segments) && i < 3; i++ {
		fmt.Sscanf(segments[i], "%d", &parts[i])
	}
	return parts
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, 0755)
}

func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}
