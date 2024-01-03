package main

import (
    "crypto/md5"
    "encoding/hex"
    "strconv"
    "strings"
    "gopkg.in/yaml.v2"
    "io/ioutil"
    "log"
    "os"
    "os/exec"
    "github.com/fsnotify/fsnotify"
	"bufio"
    "bytes"
)

type Config struct {
    ProgramSettings struct {
        LogFilePath string `yaml:"log_file_path"`
        Logging struct {
            Info  bool `yaml:"info"`
            Error bool `yaml:"error"`
            Debug bool `yaml:"debug"`
        } `yaml:"logging"`
    } `yaml:"program_settings"`
    GRETunnels []struct {
        Name      string `yaml:"name"`
        LocalIP   string `yaml:"local_ip"`
        RemoteIP  string `yaml:"remote_ip"`
        TunnelIP  string `yaml:"tunnel_ip"`
        SubnetMask string `yaml:"subnet_mask"`
    } `yaml:"gre_tunnels"`
    StaticRoutes []struct {
        Destination string `yaml:"destination"`
        Gateway     string `yaml:"gateway"`
    } `yaml:"static_routes"`
    ECMPRoutes []struct {
        Route    string `yaml:"route"`
        Table    string `yaml:"table"`
        Nexthops []struct {
            Dev    string `yaml:"dev"`
            Via    string `yaml:"via"`
            Weight int    `yaml:"weight"`
        } `yaml:"nexthops"`
    } `yaml:"ecmp_routes"`
}

var (
    currentHash string
    logger      *log.Logger
    config      Config
)

func initLogger(logFilePath string, infoEnabled, errorEnabled, debugEnabled bool) {
    logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
    if err != nil {
        log.Fatalf("Failed to open log file: %v", err)
    }
    logger = log.New(logFile, "GRE-Manager: ", log.LstdFlags|log.Lshortfile)
    logger.SetOutput(logWriter{log.New(os.Stdout, "", 0), log.New(logFile, "", 0), infoEnabled, errorEnabled, debugEnabled})
}

type logWriter struct {
    stdoutLogger *log.Logger
    fileLogger   *log.Logger
    infoEnabled  bool
    errorEnabled bool
    debugEnabled bool
}

func (l logWriter) Write(p []byte) (n int, err error) {
    message := string(p)
    if l.infoEnabled || l.errorEnabled || l.debugEnabled {
        l.stdoutLogger.Print(message) // Print to stdout
        err = l.fileLogger.Output(2, message) // Also log to file
    }
    return len(p), err // Return the length of p and the error
}

func execCommand(command string, args ...string) (string, error) {
    cmd := exec.Command(command, args...)
    var out bytes.Buffer
    cmd.Stdout = &out
    cmd.Stderr = os.Stderr
    err := cmd.Run()
    return out.String(), err
}

func tunnelExists(name string) bool {
    output, _ := execCommand("ip", "tunnel", "show")
    return strings.Contains(output, name)
}

func setupGRETunnels() error {
    for _, tunnel := range config.GRETunnels {
        if tunnelExists(tunnel.Name) {
            _, err := execCommand("ip", "tunnel", "del", tunnel.Name)
            if err != nil {
                logger.Printf("Failed to delete tunnel %s: %v", tunnel.Name, err)
            }
        }

        _, err := execCommand("ip", "tunnel", "add", tunnel.Name, "mode", "gre", "local", tunnel.LocalIP, "remote", tunnel.RemoteIP)
        if err != nil {
            return err
        }
        _, err = execCommand("ip", "addr", "add", tunnel.TunnelIP+"/"+tunnel.SubnetMask, "dev", tunnel.Name)
        if err != nil {
            return err
        }
        _, err = execCommand("ip", "link", "set", tunnel.Name, "up")
        if err != nil {
            return err
        }
        logger.Printf("Configured tunnel: %s", tunnel.Name)
    }
    return nil
}

func routeExists(destination, gateway string) bool {
    output, _ := execCommand("ip", "route", "show")
    return strings.Contains(output, destination) && strings.Contains(output, gateway)
}

func setupStaticRoutes() error {
    for _, route := range config.StaticRoutes {
        if !routeExists(route.Destination, route.Gateway) {
            if _, err := execCommand("ip", "route", "add", route.Destination, "via", route.Gateway); err != nil {
                logger.Printf("Failed to add static route %s via %s: %v", route.Destination, route.Gateway, err)
            } else {
                logger.Printf("Added static route: %s via %s", route.Destination, route.Gateway)
            }
        }
    }
    return nil
}

func ecmpRouteExists(route, table string) bool {
    output, _ := execCommand("ip", "route", "show", "table", table)
    scanner := bufio.NewScanner(strings.NewReader(output))
    for scanner.Scan() {
        if strings.Contains(scanner.Text(), route) {
            return true
        }
    }
    return false
}

func setupECMPRoutes() error {
    for _, ecmp := range config.ECMPRoutes {
        if !ecmpRouteExists(ecmp.Route, ecmp.Table) {
            var nexthopArgs []string
            for _, nh := range ecmp.Nexthops {
                nexthopArgs = append(nexthopArgs, "nexthop", "dev", nh.Dev, "via", nh.Via, "weight", strconv.Itoa(nh.Weight))
            }
            args := append([]string{"route", "add", ecmp.Route, "proto", "static", "scope", "global", "table", ecmp.Table}, nexthopArgs...)
            if _, err := execCommand("ip", args...); err != nil {
                logger.Printf("Failed to add ECMP route %s: %v", ecmp.Route, err)
            } else {
                logger.Printf("Added ECMP route: %s", strings.Join(args, " "))
            }
        }
    }
    return nil
}

func getFileMD5(filePath string) (string, error) {
    var md5String string
    data, err := ioutil.ReadFile(filePath)
    if err != nil {
        return md5String, err
    }
    hash := md5.Sum(data)
    md5String = hex.EncodeToString(hash[:])
    return md5String, nil
}

func loadConfig(filePath string) error {
    data, err := ioutil.ReadFile(filePath)
    if err != nil {
        return err
    }

    if err := yaml.Unmarshal(data, &config); err != nil {
        return err
    }

    return nil
}

func watchConfigFile(filePath string) {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        logger.Fatal(err)
    }
    defer watcher.Close()

    done := make(chan bool)
    go func() {
        for {
            select {
            case event, ok := <-watcher.Events:
                if !ok {
                    return
                }
                if event.Op&fsnotify.Write == fsnotify.Write {
                    newHash, err := getFileMD5(filePath)
                    if err != nil {
                        logger.Println("Error reading file:", err)
                        continue
                    }
                    if newHash != currentHash {
                        currentHash = newHash
                        if err := loadConfig(filePath); err != nil {
                            logger.Printf("Error loading config: %v", err)
                            continue
                        }
                        if err := setupGRETunnels(); err != nil {
                            logger.Printf("Error setting up GRE tunnels: %v", err)
                        }
                        if err := setupStaticRoutes(); err != nil {
                            logger.Printf("Error setting up static routes: %v", err)
                        }
                        if err := setupECMPRoutes(); err != nil {
                            logger.Printf("Error setting up ECMP routes: %v", err)
                        }
                    }
                }
            case err, ok := <-watcher.Errors:
                if !ok {
                    return
                }
                logger.Println("Error:", err)
            }
        }
    }()

    err = watcher.Add(filePath)
    if err != nil {
        logger.Fatal(err)
    }
    <-done
}

func main() {
    configFilePath := "/etc/krouter/config.yml"
    
    if err := loadConfig(configFilePath); err != nil {
        log.Fatalf("Error loading initial config: %v", err)
    }

    initLogger(config.ProgramSettings.LogFilePath, config.ProgramSettings.Logging.Info, config.ProgramSettings.Logging.Error, config.ProgramSettings.Logging.Debug)

    if err := setupGRETunnels(); err != nil {
        logger.Fatalf("Error setting up initial GRE tunnels: %v", err)
    }

    if err := setupStaticRoutes(); err != nil {
        logger.Fatalf("Error setting up initial static routes: %v", err)
    }

    if err := setupECMPRoutes(); err != nil {
        logger.Fatalf("Error setting up initial ECMP routes: %v", err)
    }

    hash, err := getFileMD5(configFilePath)
    if err != nil {
        logger.Fatalf("Error computing initial file hash: %v", err)
    }
    currentHash = hash

    watchConfigFile(configFilePath)
}
