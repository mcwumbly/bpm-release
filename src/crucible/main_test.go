package main_test

import (
	"crucible/config"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"code.cloudfoundry.org/bytefmt"

	yaml "gopkg.in/yaml.v2"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	uuid "github.com/satori/go.uuid"
)

var _ = Describe("Crucible", func() {
	var (
		command *exec.Cmd

		boshConfigPath,
		jobName,
		procName,
		containerID,
		jobConfigPath,
		stdoutFileLocation,
		stderrFileLocation string

		jobConfig *config.CrucibleConfig
	)

	var writeConfig = func(cfg *config.CrucibleConfig) {
		jobConfigDir := filepath.Join(boshConfigPath, "jobs", jobName, "config")
		err := os.MkdirAll(jobConfigDir, 0755)
		Expect(err).NotTo(HaveOccurred())

		jobConfigPath = filepath.Join(jobConfigDir, "crucible.yml")
		Expect(os.RemoveAll(jobConfigPath)).To(Succeed())
		f, err := os.OpenFile(
			jobConfigPath,
			os.O_RDWR|os.O_CREATE,
			0644,
		)
		Expect(err).NotTo(HaveOccurred())

		data, err := yaml.Marshal(cfg)
		Expect(err).NotTo(HaveOccurred())

		n, err := f.Write(data)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(len(data)))
	}

	BeforeEach(func() {
		var err error
		boshConfigPath, err = ioutil.TempDir("", "crucible-main-test")
		Expect(err).NotTo(HaveOccurred())

		err = os.MkdirAll(filepath.Join(boshConfigPath, "packages"), 0755)
		Expect(err).NotTo(HaveOccurred())

		err = os.MkdirAll(filepath.Join(boshConfigPath, "data", "packages"), 0755)
		Expect(err).NotTo(HaveOccurred())

		err = os.MkdirAll(filepath.Join(boshConfigPath, "packages", "runc", "bin"), 0755)
		Expect(err).NotTo(HaveOccurred())

		runcPath, err := exec.LookPath("runc")
		Expect(err).NotTo(HaveOccurred())

		err = os.Link(runcPath, filepath.Join(boshConfigPath, "packages", "runc", "bin", "runc"))
		Expect(err).NotTo(HaveOccurred())

		jobName = fmt.Sprintf("crucible-test-%s", uuid.NewV4().String())
		procName = "sleeper-agent"
		containerID = fmt.Sprintf("%s-%s", jobName, procName)

		jobConfig = &config.CrucibleConfig{
			Name:       procName,
			Executable: "/bin/bash",
			Args: []string{
				"-c",
				//This script traps the SIGTERM signal and kills the subsequent
				//commands referenced by the PID in the $child variable
				`trap "echo Signalled && kill -9 $child" SIGTERM;
					 echo Foo is $FOO &&
					  (>&2 echo "$FOO is Foo") &&
					  sleep 5 &
					 child=$!;
					 wait $child`,
			},
			Env: []string{"FOO=BAR"},
		}

		stdoutFileLocation = filepath.Join(boshConfigPath, "sys", "log", jobName, procName+".out.log")
		stderrFileLocation = filepath.Join(boshConfigPath, "sys", "log", jobName, procName+".err.log")
		writeConfig(jobConfig)
	})

	AfterEach(func() {
		// using force, as we cannot delete a running container.
		cmd := exec.Command("runc", "delete", "--force", containerID)
		combinedOutput, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(combinedOutput))

		err = os.RemoveAll(boshConfigPath)
		Expect(err).NotTo(HaveOccurred())
	})

	runcState := func(cid string) specs.State {
		cmd := exec.Command("runc", "state", cid)
		cmd.Stderr = GinkgoWriter

		data, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())

		stateResponse := specs.State{}
		err = json.Unmarshal(data, &stateResponse)
		Expect(err).NotTo(HaveOccurred())

		return stateResponse
	}

	Context("start", func() {
		JustBeforeEach(func() {
			command = exec.Command(cruciblePath, "start", "-j", jobName, "-c", jobConfigPath)
			command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))
		})

		It("runs the process in a container with a pidfile", func() {
			session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(session).Should(gexec.Exit(0))

			state := runcState(containerID)
			Expect(state.Status).To(Equal("running"))
			pidText, err := ioutil.ReadFile(filepath.Join(boshConfigPath, "sys", "run", "crucible", jobName, fmt.Sprintf("%s.pid", procName)))
			Expect(err).NotTo(HaveOccurred())

			pid, err := strconv.Atoi(string(pidText))
			Expect(err).NotTo(HaveOccurred())
			Expect(pid).To(Equal(state.Pid))
		})

		It("redirects stdout and stderr to a standard location", func() {
			Expect(stdoutFileLocation).NotTo(BeAnExistingFile())
			Expect(stderrFileLocation).NotTo(BeAnExistingFile())

			session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(session).Should(gexec.Exit(0))

			Eventually(fileContents(stdoutFileLocation)).Should(Equal("Foo is BAR\n"))
			Eventually(fileContents(stderrFileLocation)).Should(Equal("BAR is Foo\n"))
		})

		Context("resource limits", func() {
			Context("memory", func() {
				var limitInBytes uint64

				BeforeEach(func() {
					jobConfig.Executable = "/bin/bash"
					jobConfig.Args = []string{
						"-c",
						// See https://codegolf.stackexchange.com/questions/24485/create-a-memory-leak-without-any-fork-bombs
						`:(){ : $@$@;};: :`,
					}

					limit := "10M"
					jobConfig.Limits = &config.Limits{
						Memory: limit,
					}

					var err error
					limitInBytes, err = bytefmt.ToBytes(limit)
					Expect(err).NotTo(HaveOccurred())

					writeConfig(jobConfig)
				})

				It("gets OOMed when it exceeds its memory limit", func() {
					session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(session).Should(gexec.Exit(0))

					Eventually(func() string {
						return runcState(containerID).Status
					}).Should(Equal("running"))

					Eventually(func() uint64 {
						eventsCmd := exec.Command("runc", "events", containerID, "--stats")
						data, err := eventsCmd.CombinedOutput()
						Expect(err).NotTo(HaveOccurred())

						event := containerStatsEvent{}
						err = json.Unmarshal(data, &event)
						Expect(err).NotTo(HaveOccurred())

						return event.Data.Memory.Usage.Usage
					}).Should(BeNumerically("~", limitInBytes, 1000))

					Consistently(func() uint64 {
						eventsCmd := exec.Command("runc", "events", containerID, "--stats")
						data, err := eventsCmd.CombinedOutput()
						Expect(err).NotTo(HaveOccurred())

						event := containerStatsEvent{}
						err = json.Unmarshal(data, &event)
						Expect(err).NotTo(HaveOccurred())

						return event.Data.Memory.Usage.Usage
					}).Should(BeNumerically("<=", limitInBytes))
				})
			})
		})

		Context("when the stdout and stderr files already exist", func() {
			BeforeEach(func() {
				Expect(os.MkdirAll(filepath.Dir(stdoutFileLocation), 0700)).To(Succeed())
				Expect(ioutil.WriteFile(stdoutFileLocation, []byte("STDOUT PREFIX: "), 0700)).To(Succeed())
				Expect(ioutil.WriteFile(stderrFileLocation, []byte("STDERR PREFIX: "), 0700)).To(Succeed())
			})

			It("does not truncate the file", func() {
				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(session).Should(gexec.Exit(0))

				Eventually(fileContents(stdoutFileLocation)).Should(Equal("STDOUT PREFIX: Foo is BAR\n"))
				Eventually(fileContents(stderrFileLocation)).Should(Equal("STDERR PREFIX: BAR is Foo\n"))
			})
		})

		Context("when the crucible configuration file does not exist", func() {
			It("exit with a non-zero exit code and prints an error", func() {
				command = exec.Command(cruciblePath, "stop", "-j", jobName, "-c", "i am a bogus config path")
				command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				Expect(session.Err).Should(gbytes.Say(
					"Error: failed to load config at %s: ",
					"i am a bogus config path",
				))
			})
		})

		Context("when no job name is specified", func() {
			It("exits with a non-zero exit code and prints the usage", func() {
				command = exec.Command(cruciblePath, "start")
				command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				Expect(session.Err).Should(gbytes.Say("must specify a job"))
			})
		})

		Context("when no config is specified", func() {
			It("exits with a non-zero exit code and prints the usage", func() {
				command = exec.Command(cruciblePath, "start", "-j", jobName)
				command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				Expect(session.Err).Should(gbytes.Say("must specify a configuration file"))
			})
		})

		Context("when starting the job fails", func() {
			BeforeEach(func() {
				start := exec.Command(cruciblePath, "start", "-j", jobName, "-c", jobConfigPath)
				start.Env = append(start.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(start, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(0))
			})

			It("cleans up the associated container and artifacts", func() {
				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				_, err = os.Open(filepath.Join(boshConfigPath, "data", "crucible", "bundles", jobName, procName))
				Expect(err).To(HaveOccurred())
				Expect(os.IsNotExist(err)).To(BeTrue())

				cmd := exec.Command("runc", "state", containerID)
				Expect(cmd.Run()).To(HaveOccurred())
			})
		})
	})

	Context("stop", func() {
		BeforeEach(func() {
			startCmd := exec.Command(cruciblePath, "start", "-j", jobName, "-c", jobConfigPath)
			startCmd.Env = append(startCmd.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

			session, err := gexec.Start(startCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).ShouldNot(HaveOccurred())
			Eventually(session).Should(gexec.Exit(0))
		})

		JustBeforeEach(func() {
			command = exec.Command(cruciblePath, "stop", "-j", jobName, "-c", jobConfigPath)
			command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))
		})

		It("signals the container with a SIGTERM", func() {
			session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(err).ToNot(HaveOccurred())
			Eventually(session).Should(gexec.Exit(0))

			Eventually(fileContents(stdoutFileLocation)).Should(ContainSubstring("Signalled"))
		})

		It("removes the container and it's corresponding process", func() {
			session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(err).ToNot(HaveOccurred())
			Eventually(session).Should(gexec.Exit(0))

			cmd := exec.Command("runc", "state", containerID)
			err = cmd.Run()
			Expect(err).To(HaveOccurred())
		})

		It("removes the bundle directory", func() {
			session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(session).Should(gexec.Exit(0))

			_, err = os.Open(filepath.Join(boshConfigPath, "data", "crucible", "bundles", jobName, procName))
			Expect(err).To(HaveOccurred())
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		Context("when the job name is not specified", func() {
			It("exits with a non-zero exit code and prints the usage", func() {
				command = exec.Command(cruciblePath, "stop")
				command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				Expect(session.Err).Should(gbytes.Say("must specify a job"))
			})
		})

		Context("when no config is specified", func() {
			It("exits with a non-zero exit code and prints the usage", func() {
				command = exec.Command(cruciblePath, "stop", "-j", jobName)
				command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				Expect(session.Err).Should(gbytes.Say("must specify a configuration file"))
			})
		})

		Context("when the crucible configuration file does not exist", func() {
			It("exit with a non-zero exit code and prints an error", func() {
				command = exec.Command(cruciblePath, "stop", "-j", jobName, "-c", "i am a bogus config path")
				command.Env = append(command.Env, fmt.Sprintf("CRUCIBLE_BOSH_ROOT=%s", boshConfigPath))

				session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
				Expect(err).ShouldNot(HaveOccurred())
				Eventually(session).Should(gexec.Exit(1))

				Expect(session.Err).Should(gbytes.Say(
					"Error: failed to load config at %s: ",
					"i am a bogus config path",
				))
			})
		})
	})

	Context("when no flags are provided", func() {
		It("exits with a non-zero exit code and prints the usage", func() {
			command := exec.Command(cruciblePath)
			session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
			Expect(err).ShouldNot(HaveOccurred())
			Eventually(session).Should(gexec.Exit(1))

			Expect(session.Err).Should(gbytes.Say("Usage:"))
		})
	})
})

func fileContents(path string) func() string {
	return func() string {
		data, err := ioutil.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}
}

type containerStatsEvent struct {
	Data containerStats `json:"data"`
}

type containerStats struct {
	Memory memory `json:"memory"`
}

type memory struct {
	Cache     uint64            `json:"cache,omitempty"`
	Usage     memoryEntry       `json:"usage,omitempty"`
	Swap      memoryEntry       `json:"swap,omitempty"`
	Kernel    memoryEntry       `json:"kernel,omitempty"`
	KernelTCP memoryEntry       `json:"kernelTCP,omitempty"`
	Raw       map[string]uint64 `json:"raw,omitempty"`
}

type memoryEntry struct {
	Limit   uint64 `json:"limit"`
	Usage   uint64 `json:"usage,omitempty"`
	Max     uint64 `json:"max,omitempty"`
	Failcnt uint64 `json:"failcnt"`
}
