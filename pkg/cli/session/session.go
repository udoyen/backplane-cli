package session

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openshift/backplane-cli/cmd/ocm-backplane/login"
	"github.com/openshift/backplane-cli/pkg/cli/config"
	"github.com/openshift/backplane-cli/pkg/info"
	"github.com/openshift/backplane-cli/pkg/utils"
	"github.com/spf13/cobra"
)

// BackplaneSessionInterface abstract backplane session functions
type BackplaneSessionInterface interface {
	RunCommand(cmd *cobra.Command, args []string) error
	Setup() error
	Start() error
	Delete() error
}

// BackplaneSession struct for default Backplane session
type BackplaneSession struct {
	Path    string
	Exists  bool
	Options *Options
}

// Options define deafult backplane session options
type Options struct {
	DeleteSession bool

	Alias string

	ClusterId   string
	ClusterName string
}

var (
	DefaultBackplaneSession BackplaneSessionInterface = &BackplaneSession{}
)

// RunCommand setup session and allows to execute commands
func (e *BackplaneSession) RunCommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		e.Options.Alias = args[0]
	}
	if e.Options.ClusterId == "" && e.Options.Alias == "" {
		return fmt.Errorf("ClusterId or Alias required")
	}

	if e.Options.Alias == "" {
		log.Println("No Alias set, using cluster ID")
		e.Options.Alias = e.Options.ClusterId
	}

	// Verify validity of the ClusterID
	clusterKey := e.Options.Alias
	if e.Options.ClusterId != "" {
		clusterKey = e.Options.ClusterId
	}

	clusterId, clusterName, err := utils.DefaultOCMInterface.GetTargetCluster(clusterKey)

	if err != nil {
		return fmt.Errorf("invalid cluster Id %s", clusterKey)
	}

	// set cluster options
	e.Options.ClusterName = clusterName
	e.Options.ClusterId = clusterId

	err = e.initSessionPath()
	if err != nil {
		return fmt.Errorf("could not init session path")
	}

	if e.Options.DeleteSession {
		fmt.Printf("Cleaning up Backplane session %s\n", e.Options.Alias)
		err = e.Delete()
		if err != nil {
			return fmt.Errorf("could not delete the session. error: %v", err)
		}
		return nil
	}

	err = e.Setup()
	if err != nil {
		return fmt.Errorf("could not setup session. error: %v", err)
	}

	// Init cluster login via cluster ID or Alias
	err = e.initClusterLogin(cmd)
	if err != nil {
		return fmt.Errorf("could not login to the cluster. error: %v", err)
	}

	err = e.Start()
	if err != nil {
		return fmt.Errorf("could not start session. error: %v", err)
	}
	return nil
}

// Setup intitate the sessoion environment
func (e *BackplaneSession) Setup() error {
	// Delete session if exist
	err := e.Delete()
	if err != nil {
		return fmt.Errorf("error deleting session. error: %v", err)
	}

	err = e.ensureEnvDir()
	if err != nil {
		return fmt.Errorf("error validating env directory. error: %v", err)
	}

	e.printSessionHeader()

	// Create session Bins
	err = e.createBins()
	if err != nil {
		return fmt.Errorf("error creating bins. error: %v", err)
	}

	// Creating history files
	err = e.createHistoryFile()
	if err != nil {
		return fmt.Errorf("error creating history files. error: %v", err)
	}

	// Validaing env variables
	err = e.ensureEnvVariables()
	if err != nil {
		return fmt.Errorf("error setting env vars. error: %v", err)
	}

	return nil
}

// Start trigger the session start
func (e *BackplaneSession) Start() error {
	shell := os.Getenv("SHELL")

	if shell != "" {
		fmt.Print("Switching to Backplane session " + e.Options.Alias + "\n")
		cmd := exec.Command(shell)

		path := filepath.Clean(e.Path + "/.ocenv")
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			if err := file.Close(); err != nil {
				fmt.Println("Error closing file: ", path)
				return
			}
		}()
		scanner := bufio.NewScanner(file)
		cmd.Env = os.Environ()
		for scanner.Scan() {
			line := scanner.Text()
			cmd.Env = append(cmd.Env, line)
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = e.Path
		err = cmd.Run()
		if err != nil {
			return fmt.Errorf("error while running cmd. %v", err)
		}

		fmt.Printf("Exited Backplane session \n")
	}

	return nil
}

// Delete cleanup the backplane session
func (e *BackplaneSession) Delete() error {
	err := os.RemoveAll(e.Path)
	if err != nil {
		fmt.Println("error while calling os.RemoveAll", err.Error())

	}
	return nil
}

// ensureEnvDir create session dirs if it's not exist
func (e *BackplaneSession) ensureEnvDir() error {
	if _, err := os.Stat(e.Path); errors.Is(err, os.ErrNotExist) {
		err := os.MkdirAll(e.Path, os.ModePerm)
		if err != nil {
			return err
		}
	}
	e.Exists = true
	return nil
}

// ensureEnvDir intiate session env vars
func (e *BackplaneSession) ensureEnvVariables() error {
	envContent := `
HISTFILE=` + e.Path + `/.history
PATH=` + e.Path + `/bin:` + os.Getenv("PATH") + `
`

	if e.Options.ClusterId != "" {
		clusterEnvContent := "KUBECONFIG=" + filepath.Join(e.Path, e.Options.ClusterId, "config") + "\n"
		clusterEnvContent = clusterEnvContent + "CLUSTERID=" + e.Options.ClusterId + "\n"
		clusterEnvContent = clusterEnvContent + "CLUSTERNAME=" + e.Options.ClusterName + "\n"
		envContent = envContent + clusterEnvContent
	}
	direnvfile, err := e.ensureFile(e.Path + "/.ocenv")
	if err != nil {
		return err
	}
	_, err = direnvfile.WriteString(envContent)
	if err != nil {
		log.Fatal(err)
	}
	defer func(direnvfile *os.File) {
		direnvfile.Close()
	}(direnvfile)

	zshenvfile, err := e.ensureFile(e.Path + "/.zshenv")
	if err != nil {
		return err
	}
	_, err = zshenvfile.WriteString("source .ocenv")
	if err != nil {
		log.Fatal(err)
	}
	defer func(direnvfile *os.File) {
		err := direnvfile.Close()
		if err != nil {
			fmt.Println("Error while calling direnvFile.Close(): ", err.Error())
			return
		}
	}(direnvfile)
	return nil
}

// createHistoryFile create .history file inside the session folder
func (e *BackplaneSession) createHistoryFile() error {
	historyFile := filepath.Join(e.Path, "/.history")
	scriptFile, err := e.ensureFile(historyFile)
	if err != nil {
		return err
	}
	defer func(scriptfile *os.File) {
		err := scriptfile.Close()
		if err != nil {
			fmt.Println("Error closing file: ", historyFile)
			return
		}
	}(scriptFile)
	return nil
}

// createBins create bins inside the session folder bin dir
func (e *BackplaneSession) createBins() error {
	if _, err := os.Stat(e.binPath()); errors.Is(err, os.ErrNotExist) {
		err := os.Mkdir(e.binPath(), os.ModePerm)
		if err != nil {
			log.Fatal(err)
		}
	}
	err := e.createBin("ocd", "ocm describe cluster "+e.Options.ClusterId)
	if err != nil {
		return err
	}
	ocb := `
#!/bin/bash

set -euo pipefail

`
	err = e.createBin("ocb", ocb)
	if err != nil {
		return err
	}
	return nil
}

// createBin create bin file with given content
func (e *BackplaneSession) createBin(cmd string, content string) error {
	path := filepath.Join(e.binPath(), cmd)
	scriptfile, err := e.ensureFile(path)
	if err != nil {
		return err
	}
	defer func(scriptfile *os.File) {
		err := scriptfile.Close()
		if err != nil {
			fmt.Println("Error closing file: ", path)
			return
		}
	}(scriptfile)
	_, err = scriptfile.WriteString(content)
	if err != nil {
		return fmt.Errorf("error writing to file %s: %v", path, err)
	}
	err = os.Chmod(path, 0700)
	if err != nil {
		return fmt.Errorf("can't update permissions on file %s: %v", path, err)
	}
	return nil
}

// ensureFile check the existance of file in session path
func (e *BackplaneSession) ensureFile(filename string) (file *os.File, err error) {
	filename = filepath.Clean(filename)
	if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) {
		file, err = os.Create(filename)
		if err != nil {
			return nil, fmt.Errorf("can't create file %s: %v", filename, err)
		}
	}
	return file, nil
}

// binPath returns the session bin path
func (e *BackplaneSession) binPath() string {
	return e.Path + "/bin"
}

// initSessionPath initialise the session saving path based on the user config
func (e *BackplaneSession) initSessionPath() error {

	if e.Path == "" {
		bpConfig, err := config.GetBackplaneConfiguration()
		if err != nil {
			return err
		}
		sessionDir := info.BACKPLANE_DEFAULT_SESSION_DIRECTORY

		// Get the session directory name via config
		if bpConfig.SessionDirectory != "" {
			sessionDir = bpConfig.SessionDirectory
		}

		userHomeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		e.Path = filepath.Join(userHomeDir, sessionDir, e.Options.Alias)
	}

	// Add Alias to the path
	if !strings.Contains(e.Path, e.Options.Alias) {
		e.Path = filepath.Join(e.Path, e.Options.Alias)
	}

	return nil
}

// initCluster login to cluster and save kube config into session for valid clusters
func (e *BackplaneSession) initClusterLogin(cmd *cobra.Command) error {

	if e.Options.ClusterId != "" {

		// Setting up the flags
		err := login.LoginCmd.Flags().Set("multi", "true")
		if err != nil {
			return fmt.Errorf("error occered when setting multi flag %v", err)
		}
		err = login.LoginCmd.Flags().Set("kube-path", e.Path)
		if err != nil {
			return fmt.Errorf("error occered when kube-path flag %v", err)
		}

		// Execute login command
		err = login.LoginCmd.RunE(cmd, []string{e.Options.ClusterId})
		if err != nil {
			return fmt.Errorf("error occered when login to the cluster %v", err)
		}
	}

	return nil
}

// printSessionHeader prints backplane session title and help
func (e *BackplaneSession) printSessionHeader() {
	fmt.Println("========================================================================")
	fmt.Println("*          Backplane Session                                           *")
	fmt.Println("*                                                                      *")
	fmt.Println("*Help:                                                                 *")
	fmt.Println("* type \"exit\" to terminate the current session                         *")
	fmt.Println("* You can use oc commands to interact with cluster                     *")
	fmt.Println("*                                                                      *")
	fmt.Println("* If the session is not initialized in the cluster env automatically   *")
	fmt.Println("* then executes \"source .ocenv\" enable it manually                     *")
	fmt.Println("========================================================================")
}
