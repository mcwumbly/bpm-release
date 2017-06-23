package runcadapter_test

import (
	"crucible/config"
	"crucible/runcadapter"
	"crucible/runcadapter/runcadapterfakes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/clock/fakeclock"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

var _ = Describe("RuncJobLifecycle", func() {
	var (
		fakeRuncAdapter  *runcadapterfakes.FakeRuncAdapter
		fakeRuncClient   *runcadapterfakes.FakeRuncClient
		fakeUserIDFinder *runcadapterfakes.FakeUserIDFinder

		jobConfig *config.CrucibleConfig
		jobSpec   specs.Spec

		expectedJobName,
		expectedProcName,
		expectedContainerID,
		expectedSystemRoot,
		expectedPidDir string

		expectedStdout, expectedStderr *os.File

		expectedUser specs.User

		fakeClock *fakeclock.FakeClock

		runcLifecycle *runcadapter.RuncJobLifecycle
	)

	BeforeEach(func() {
		fakeClock = fakeclock.NewFakeClock(time.Now())
		fakeRuncAdapter = &runcadapterfakes.FakeRuncAdapter{}
		fakeRuncClient = &runcadapterfakes.FakeRuncClient{}
		fakeUserIDFinder = &runcadapterfakes.FakeUserIDFinder{}

		expectedUser = specs.User{Username: "vcap", UID: 300, GID: 400}
		fakeUserIDFinder.LookupReturns(expectedUser, nil)

		var err error
		expectedPidDir = "a-pid-dir"
		expectedStdout, err = ioutil.TempFile("", "runc-lifecycle-stdout")
		Expect(err).NotTo(HaveOccurred())
		expectedStderr, err = ioutil.TempFile("", "runc-lifecycle-stderr")
		Expect(err).NotTo(HaveOccurred())
		fakeRuncAdapter.CreateJobPrerequisitesReturns(expectedPidDir, expectedStdout, expectedStderr, nil)

		expectedJobName = "example"
		expectedProcName = "server"
		expectedContainerID = fmt.Sprintf("%s-%s", expectedJobName, expectedProcName)

		jobConfig = &config.CrucibleConfig{
			Name:       expectedProcName,
			Executable: "/bin/sleep",
		}
		jobSpec = specs.Spec{
			Version: "example-version",
		}
		fakeRuncAdapter.BuildSpecReturns(jobSpec, nil)

		expectedSystemRoot = "system-root"

		runcLifecycle = runcadapter.NewRuncJobLifecycle(
			fakeRuncClient,
			fakeRuncAdapter,
			fakeUserIDFinder,
			fakeClock,
			expectedSystemRoot,
			expectedJobName,
			jobConfig,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(expectedStdout.Name())).To(Succeed())
		Expect(os.RemoveAll(expectedStderr.Name())).To(Succeed())
	})

	Describe("StartJob", func() {
		It("builds the runc spec, bundle, and runs the container", func() {
			err := runcLifecycle.StartJob()
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeUserIDFinder.LookupCallCount()).To(Equal(1))
			Expect(fakeUserIDFinder.LookupArgsForCall(0)).To(Equal(runcadapter.VCAP_USER))

			Expect(fakeRuncAdapter.CreateJobPrerequisitesCallCount()).To(Equal(1))
			systemRoot, jobName, cfg, user := fakeRuncAdapter.CreateJobPrerequisitesArgsForCall(0)
			Expect(systemRoot).To(Equal(expectedSystemRoot))
			Expect(jobName).To(Equal(expectedJobName))
			Expect(cfg).To(Equal(jobConfig))
			Expect(user).To(Equal(expectedUser))

			Expect(fakeRuncAdapter.BuildSpecCallCount()).To(Equal(1))
			systemRoot, jobName, cfg, user = fakeRuncAdapter.BuildSpecArgsForCall(0)
			Expect(systemRoot).To(Equal(expectedSystemRoot))
			Expect(jobName).To(Equal(expectedJobName))
			Expect(cfg).To(Equal(jobConfig))
			Expect(user).To(Equal(expectedUser))

			Expect(fakeRuncClient.CreateBundleCallCount()).To(Equal(1))
			bundlePath, spec, user := fakeRuncClient.CreateBundleArgsForCall(0)
			Expect(bundlePath).To(Equal(filepath.Join(expectedSystemRoot, "data", "crucible", "bundles", expectedJobName, expectedProcName)))
			Expect(spec).To(Equal(jobSpec))
			Expect(user).To(Equal(expectedUser))

			Expect(fakeRuncClient.RunContainerCallCount()).To(Equal(1))
			pidFilePath, bundlePath, cid, stdout, stderr := fakeRuncClient.RunContainerArgsForCall(0)
			Expect(pidFilePath).To(Equal(filepath.Join(expectedPidDir, fmt.Sprintf("%s.pid", expectedProcName))))
			Expect(bundlePath).To(Equal(filepath.Join(expectedSystemRoot, "data", "crucible", "bundles", expectedJobName, expectedProcName)))
			Expect(cid).To(Equal(expectedContainerID))
			Expect(stdout).To(Equal(expectedStdout))
			Expect(stderr).To(Equal(expectedStderr))
		})

		Context("when looking up the vcap user fails", func() {
			BeforeEach(func() {
				fakeUserIDFinder.LookupReturns(specs.User{}, errors.New("boom"))
			})

			It("returns an error", func() {
				err := runcLifecycle.StartJob()
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when creating the system files fails", func() {
			BeforeEach(func() {
				fakeRuncAdapter.CreateJobPrerequisitesReturns("", nil, nil, errors.New("boom"))
			})

			It("returns an error", func() {
				err := runcLifecycle.StartJob()
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when building the runc spec fails", func() {
			BeforeEach(func() {
				fakeRuncAdapter.BuildSpecReturns(specs.Spec{}, errors.New("boom"))
			})

			It("returns an error", func() {
				err := runcLifecycle.StartJob()
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when building the bundle fails", func() {
			BeforeEach(func() {
				fakeRuncClient.CreateBundleReturns(errors.New("boom!"))
			})

			It("returns an error", func() {
				err := runcLifecycle.StartJob()
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when running the container fails", func() {
			BeforeEach(func() {
				fakeRuncClient.RunContainerReturns(errors.New("boom!"))
			})

			It("returns an error", func() {
				err := runcLifecycle.StartJob()
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("StopJob", func() {
		var exitTimeout time.Duration

		BeforeEach(func() {
			exitTimeout = 5 * time.Second

			fakeRuncClient.ContainerStateReturns(specs.State{
				Status: "stopped",
			}, nil)
		})

		It("stops the container", func() {
			err := runcLifecycle.StopJob(exitTimeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeRuncClient.StopContainerCallCount()).To(Equal(1))
			containerID := fakeRuncClient.StopContainerArgsForCall(0)
			Expect(containerID).To(Equal(expectedContainerID))

			Expect(fakeRuncClient.ContainerStateCallCount()).To(Equal(1))
			containerID = fakeRuncClient.ContainerStateArgsForCall(0)
			Expect(containerID).To(Equal(expectedContainerID))
		})

		Context("when the container does not stop immediately", func() {
			var stopped chan struct{}
			BeforeEach(func() {
				stopped = make(chan struct{})

				fakeRuncClient.ContainerStateStub = func(containerID string) (specs.State, error) {
					select {
					case <-stopped:
						return specs.State{Status: "stopped"}, nil
					default:
						return specs.State{Status: "running"}, nil
					}
				}
			})

			It("polls the container state every second until it stops", func() {
				errChan := make(chan error)
				go func() {
					defer GinkgoRecover()
					errChan <- runcLifecycle.StopJob(exitTimeout)
				}()

				Eventually(fakeRuncClient.StopContainerCallCount).Should(Equal(1))
				Expect(fakeRuncClient.StopContainerArgsForCall(0)).To(Equal(expectedContainerID))

				Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(1))
				Expect(fakeRuncClient.ContainerStateArgsForCall(0)).To(Equal(expectedContainerID))

				fakeClock.WaitForWatcherAndIncrement(1 * time.Second)

				Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(2))
				Expect(fakeRuncClient.ContainerStateArgsForCall(1)).To(Equal(expectedContainerID))

				close(stopped)
				fakeClock.WaitForWatcherAndIncrement(1 * time.Second)

				Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(3))
				Expect(fakeRuncClient.ContainerStateArgsForCall(2)).To(Equal(expectedContainerID))

				Eventually(errChan).Should(Receive(BeNil()), "stop job did not exit in time")
			})

			Context("and the exit timeout has passed", func() {
				It("forcefully removes the container", func() {
					errChan := make(chan error)
					go func() {
						defer GinkgoRecover()
						errChan <- runcLifecycle.StopJob(exitTimeout)
					}()

					Eventually(fakeRuncClient.StopContainerCallCount).Should(Equal(1))
					Expect(fakeRuncClient.StopContainerArgsForCall(0)).To(Equal(expectedContainerID))

					Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(1))
					Expect(fakeRuncClient.ContainerStateArgsForCall(0)).To(Equal(expectedContainerID))

					fakeClock.WaitForWatcherAndIncrement(1 * time.Second)

					Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(2))
					Expect(fakeRuncClient.ContainerStateArgsForCall(1)).To(Equal(expectedContainerID))

					fakeClock.WaitForWatcherAndIncrement(exitTimeout)

					var actualError error
					Eventually(errChan).Should(Receive(&actualError))
					Expect(actualError).To(Equal(runcadapter.TimeoutError))
				})
			})
		})

		Context("when fetching the container state fails", func() {
			BeforeEach(func() {
				fakeRuncClient.ContainerStateReturns(specs.State{}, errors.New("boom"))
			})

			It("keeps attempting to fetch the state", func() {
				errChan := make(chan error)
				go func() {
					defer GinkgoRecover()
					errChan <- runcLifecycle.StopJob(exitTimeout)
				}()

				Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(1))
				Expect(fakeRuncClient.ContainerStateArgsForCall(0)).To(Equal(expectedContainerID))

				fakeClock.WaitForWatcherAndIncrement(1 * time.Second)

				Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(2))
				Expect(fakeRuncClient.ContainerStateArgsForCall(1)).To(Equal(expectedContainerID))

				fakeClock.WaitForWatcherAndIncrement(1 * time.Second)

				Eventually(fakeRuncClient.ContainerStateCallCount).Should(Equal(3))
				Expect(fakeRuncClient.ContainerStateArgsForCall(2)).To(Equal(expectedContainerID))

				fakeClock.WaitForWatcherAndIncrement(exitTimeout)

				var actualError error
				Eventually(errChan).Should(Receive(&actualError))
				Expect(actualError).To(Equal(runcadapter.TimeoutError))
			})
		})

		Context("when stopping a container fails", func() {
			var expectedErr error

			BeforeEach(func() {
				expectedErr = errors.New("an error")
				fakeRuncClient.StopContainerReturns(expectedErr)
			})

			It("returns an error", func() {
				err := runcLifecycle.StopJob(exitTimeout)
				Expect(err).To(Equal(expectedErr))
			})
		})
	})

	Describe("RemoveJob", func() {
		It("deletes the container", func() {
			err := runcLifecycle.RemoveJob()
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeRuncClient.DeleteContainerCallCount()).To(Equal(1))
			containerID := fakeRuncClient.DeleteContainerArgsForCall(0)
			Expect(containerID).To(Equal(expectedContainerID))
		})

		It("destroys the bundle", func() {
			err := runcLifecycle.RemoveJob()
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeRuncClient.DestroyBundleCallCount()).To(Equal(1))
			bundlePath := fakeRuncClient.DestroyBundleArgsForCall(0)
			Expect(bundlePath).To(Equal(filepath.Join(expectedSystemRoot, "data", "crucible", "bundles", expectedJobName, expectedProcName)))
		})

		Context("when deleting a container fails", func() {
			var expectedErr error

			BeforeEach(func() {
				expectedErr = errors.New("an error")
				fakeRuncClient.DeleteContainerReturns(expectedErr)
			})

			It("returns an error", func() {
				err := runcLifecycle.RemoveJob()
				Expect(err).To(Equal(expectedErr))
			})
		})

		Context("when destroying a bundle fails", func() {
			It("returns an error", func() {
				expectedErr := errors.New("an error2")
				fakeRuncClient.DestroyBundleReturns(expectedErr)
				err := runcLifecycle.RemoveJob()
				Expect(err).To(Equal(expectedErr))
			})
		})
	})
})
