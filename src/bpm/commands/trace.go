// Copyright (C) 2017-Present Pivotal Software, Inc. All rights reserved.
//
// This program and the accompanying materials are made available under
// the terms of the under the Apache License, Version 2.0 (the "License”);
// you may not use this file except in compliance with the License.
//
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.  See the
// License for the specific language governing permissions and limitations
// under the License.

package commands

import (
	"bpm/bpm"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var longText = `
traces a BOSH Process

  Executes strace with the following options:
    strace -s 100 -f -y -yy -p <process-pid>

  <process-pid> is determined using the 'bpm pid' command

  Note: This command may impact performance.
`

func init() {
	traceCommand.Flags().StringVarP(&jobName, "job", "j", "", "The job name.")
	traceCommand.Flags().StringVarP(&configPath, "config", "c", "", "The path to the bpm configuration file.")
	RootCmd.AddCommand(traceCommand)
}

var traceCommand = &cobra.Command{
	Long:              longText,
	RunE:              trace,
	Short:             "traces a BOSH Process",
	Use:               "trace",
	PersistentPreRunE: tracePre,
}

func tracePre(cmd *cobra.Command, _ []string) error {
	return validateJobandConfigFlags()
}

func trace(cmd *cobra.Command, _ []string) error {
	cfg, err := bpm.ParseConfig(configPath)
	if err != nil {
		return err
	}

	runcLifecycle := newRuncLifecycle()
	job, err := runcLifecycle.GetJob(jobName, cfg)
	if err != nil {
		return fmt.Errorf("failed to get job: %s", err.Error())
	}

	if job.Pid <= 0 {
		return errors.New("no pid for job")
	}

	straceCmd := exec.Command("strace", "-s", "100", "-f", "-y", "-yy", "-p", fmt.Sprintf("%d", job.Pid))
	straceCmd.Stdin = os.Stdin
	straceCmd.Stdout = cmd.OutOrStdout()
	straceCmd.Stderr = cmd.OutOrStderr()

	return straceCmd.Run()
}