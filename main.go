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
    logger.SetOutput(logWriter{logger, infoEnabled, errorEnabled, debugEnabled})
}

type logWriter struct {
    logger *log.Logger
    infoEnabled   bool
    errorEnabled  bool
    debugEnabled  bool
}

func (l logWriter) Write(p []byte) (n int, err error) {
    if l.infoEnabled {
        return l.logger.Output(2, string(p))
    }
    return len(p), nil
}

func execCommand(command string, args ...string) error {
    cmd := exec.Command(command, args...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}

func setupGRETunnels() error {
    for _, tunnel := range config.GRETunnels {
        if err := execCommand("ip", "tunnel", "del", tunnel.Name); err != nil {
            logger.Printf("Failed to delete tunnel %s: %v", tunnel.Name, err)
        }
        if err := execCommand("ip", "tunnel", "add", tunnel.Name, "mode", "gre", "local", tunnel.LocalIP, "remote", tunnel.RemoteIP); err != nil {
            return err
        }
        if err := execCommand("ip", "addr", "add", tunnel.TunnelIP+"/"+tunnel.SubnetMask, "dev", tunnel.Name); err != nil {
            return err
        }
        if err := execCommand("ip", "link", "set", tunnel.Name, "up"); err != nil {
            return err
        }
        logger.Printf("Configured tunnel: %s", tunnel.Name)
    }
    return nil
}

func setupStaticRoutes() error {
    for _, route := range config.StaticRoutes {
        if err := execCommand("ip", "route", "add", route.Destination, "via", route.Gateway); err != nil {
            logger.Printf("Failed to add static route %s via %s: %v", route.Destination, route.Gateway, err)
        } else {
            logger.Printf("Added static route: %s via %s", route.Destination, route.Gateway)
        }
    }
    return nil
}

func setupECMPRoutes() error {
    for _, ecmp := range config.ECMPRoutes {
        var nexthopArgs []string
        for _, nh := range ecmp.Nexthops {
            nexthopArgs = append(nexthopArgs, "nexthop", "dev", nh.Dev, "via", nh.Via, "weight", strconv.Itoa(nh.Weight))
        }
        args := append([]string{"route", "add", ecmp.Route, "proto", "static", "scope", "global", "table", ecmp.Table}, nexthopArgs...)
        if err := execCommand("ip", args...); err != nil {
            logger.Printf("Failed to add ECMP route %s: %v", ecmp.Route, err)
        } else {
            logger.Printf("Added ECMP route: %s", strings.Join(args, " "))
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
    configFilePath := "/path/to/config.yml"
    
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
