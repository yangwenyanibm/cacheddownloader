package cacheddownloader_test

import (
	"crypto/md5"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	Url "net/url"
	"os"
	"sync"
	"time"

	"github.com/onsi/gomega/ghttp"

	. "github.com/pivotal-golang/cacheddownloader"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func md5HexEtag(content string) string {
	contentHash := md5.New()
	contentHash.Write([]byte(content))
	return fmt.Sprintf(`"%x"`, contentHash.Sum(nil))
}

var _ = Describe("Downloader", func() {
	var downloader Downloader
	var testServer *httptest.Server
	var serverRequestUrls []string
	var lock *sync.Mutex

	BeforeEach(func() {
		testServer = nil
		downloader = NewDownloader(100 * time.Millisecond)
		lock = &sync.Mutex{}
	})

	Describe("download", func() {
		var url *Url.URL
		var file *os.File

		BeforeEach(func() {
			serverRequestUrls = []string{}
			file, _ = ioutil.TempFile("", "foo")
		})

		AfterEach(func() {
			file.Close()

			if testServer != nil {
				testServer.Close()
			}
		})

		Context("when the download is successful", func() {
			var uploadedBytes int64
			var expectedBytes int64
			var downloadErr error
			var didDownload bool

			JustBeforeEach(func() {
				serverUrl := testServer.URL + "/somepath"
				url, _ = url.Parse(serverUrl)
				didDownload, uploadedBytes, downloadErr = downloader.Download(url, file, time.Time{})
			})

			Context("and contains a matching MD5 Hash in the Etag", func() {
				var attempts int
				BeforeEach(func() {
					attempts = 0
					testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						lock.Lock()
						serverRequestUrls = append(serverRequestUrls, r.RequestURI)
						attempts++
						lock.Unlock()

						msg := "Hello, client"
						w.Header().Set("ETag", md5HexEtag(msg))

						bytesWritten, _ := fmt.Fprint(w, msg)
						expectedBytes = int64(bytesWritten)
					}))
				})

				It("does not return an error", func() {
					Ω(downloadErr).ShouldNot(HaveOccurred())
				})

				It("only tries once", func() {
					Ω(attempts).Should(Equal(1))
				})

				It("claims to have downloaded", func() {
					Ω(didDownload).Should(BeTrue())
				})

				It("gets a file from a url", func() {
					lock.Lock()
					urlFromServer := testServer.URL + serverRequestUrls[0]
					Ω(urlFromServer).To(Equal(url.String()))
					lock.Unlock()
				})

				It("should use the provided file as the download location", func() {
					fileContents, _ := ioutil.ReadFile(file.Name())
					Ω(fileContents).Should(ContainSubstring("Hello, client"))
				})

				It("return number of bytes it downloaded", func() {
					Ω(uploadedBytes).Should(Equal(expectedBytes))
				})
			})

			Context("and contains an Etag that is not an MD5 Hash ", func() {
				BeforeEach(func() {
					testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("ETag", "not the hex you are looking for")
						bytesWritten, _ := fmt.Fprint(w, "Hello, client")
						expectedBytes = int64(bytesWritten)
					}))
				})

				It("succeeds without doing a checksum", func() {
					Ω(didDownload).Should(BeTrue())
					Ω(downloadErr).ShouldNot(HaveOccurred())
				})
			})

			Context("and contains no Etag at all", func() {
				BeforeEach(func() {
					testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						bytesWritten, _ := fmt.Fprint(w, "Hello, client")
						expectedBytes = int64(bytesWritten)
					}))
				})

				It("succeeds without doing a checksum", func() {
					Ω(didDownload).Should(BeTrue())
					Ω(downloadErr).ShouldNot(HaveOccurred())
				})
			})
		})

		Context("when the download times out", func() {
			var requestInitiated chan struct{}

			BeforeEach(func() {
				requestInitiated = make(chan struct{})

				testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestInitiated <- struct{}{}

					time.Sleep(300 * time.Millisecond)
					fmt.Fprint(w, "Hello, client")
				}))

				serverUrl := testServer.URL + "/somepath"
				url, _ = url.Parse(serverUrl)
			})

			It("should retry 3 times and return an error", func() {
				errs := make(chan error)
				didDownloads := make(chan bool)

				go func() {
					didDownload, _, err := downloader.Download(url, file, time.Time{})
					errs <- err
					didDownloads <- didDownload
				}()

				Eventually(requestInitiated).Should(Receive())
				Eventually(requestInitiated).Should(Receive())
				Eventually(requestInitiated).Should(Receive())

				Ω(<-errs).Should(HaveOccurred())
				Ω(<-didDownloads).Should(BeFalse())
			})
		})

		Context("when the download fails with a protocol error", func() {
			BeforeEach(func() {
				// No server to handle things!

				serverUrl := "http://127.0.0.1:54321/somepath"
				url, _ = url.Parse(serverUrl)
			})

			It("should return the error", func() {
				didDownload, _, err := downloader.Download(url, file, time.Time{})
				Ω(err).NotTo(BeNil())
				Ω(didDownload).Should(BeFalse())
			})
		})

		Context("when the download fails with a status code error", func() {
			BeforeEach(func() {
				testServer = httptest.NewServer(http.NotFoundHandler())

				serverUrl := testServer.URL + "/somepath"
				url, _ = url.Parse(serverUrl)
			})

			It("should return the error", func() {
				didDownload, _, err := downloader.Download(url, file, time.Time{})
				Ω(err).NotTo(BeNil())
				Ω(didDownload).Should(BeFalse())
			})
		})

		Context("when the download's ETag fails the checksum", func() {
			BeforeEach(func() {
				testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					realMsg := "Hello, client"
					incompleteMsg := "Hello, clien"

					w.Header().Set("ETag", md5HexEtag(realMsg))

					fmt.Fprint(w, incompleteMsg)
				}))

				serverUrl := testServer.URL + "/somepath"
				url, _ = url.Parse(serverUrl)
			})

			It("should return an error", func() {
				didDownload, _, err := downloader.Download(url, file, time.Time{})
				Ω(err).NotTo(BeNil())
				Ω(didDownload).Should(BeFalse())
			})
		})
	})

	Context("Downloading with a modified time", func() {
		var (
			server       *ghttp.Server
			modifiedTime time.Time
			statusCode   int
			url          *Url.URL
			body         string
			file         *os.File
		)

		BeforeEach(func() {
			modifiedTime = time.Now()
			file, _ = ioutil.TempFile("", "foo")
			server = ghttp.NewServer()
			server.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/get-the-file"),
				ghttp.VerifyHeader(http.Header{"If-Modified-Since": []string{modifiedTime.Format(http.TimeFormat)}}),
				ghttp.RespondWithPtr(&statusCode, &body),
			))

			url, _ = Url.Parse(server.URL() + "/get-the-file")
		})

		AfterEach(func() {
			server.Close()
		})

		Context("when the server replies with 304", func() {
			BeforeEach(func() {
				statusCode = http.StatusNotModified
			})

			It("should return that it did not download", func() {
				didDownload, size, err := downloader.Download(url, file, modifiedTime)
				Ω(didDownload).Should(BeFalse())
				Ω(size).Should(Equal(int64(0)))
				Ω(err).ShouldNot(HaveOccurred())
			})

			It("should not download anything", func() {
				downloader.Download(url, file, modifiedTime)
				info, err := os.Stat(file.Name())
				Ω(err).ShouldNot(HaveOccurred())
				Ω(info.Size()).Should(Equal(int64(0)))
			})
		})

		Context("when the server replies with 200", func() {
			BeforeEach(func() {
				statusCode = http.StatusOK
				body = "quarb!"
			})

			It("should return that it did download and the file size", func() {
				didDownload, size, err := downloader.Download(url, file, modifiedTime)
				Ω(didDownload).Should(BeTrue())
				Ω(size).Should(Equal(int64(len(body))))
				Ω(err).ShouldNot(HaveOccurred())
			})

			It("should download the file", func() {
				downloader.Download(url, file, modifiedTime)
				info, err := os.Stat(file.Name())
				Ω(err).ShouldNot(HaveOccurred())
				Ω(info.Size()).Should(Equal(int64(len(body))))
			})
		})

		Context("for anything else (including a server error)", func() {
			BeforeEach(func() {
				statusCode = http.StatusInternalServerError

				// cope with built in retry
				for i := 0; i < MAX_DOWNLOAD_ATTEMPTS; i++ {
					server.AppendHandlers(ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/get-the-file"),
						ghttp.VerifyHeader(http.Header{"If-Modified-Since": []string{modifiedTime.Format(http.TimeFormat)}}),
						ghttp.RespondWithPtr(&statusCode, &body),
					))
				}
			})

			It("should return false with an error", func() {
				didDownload, size, err := downloader.Download(url, file, modifiedTime)
				Ω(didDownload).Should(BeFalse())
				Ω(size).Should(Equal(int64(0)))
				Ω(err).Should(HaveOccurred())
			})

			It("should not download anything", func() {
				downloader.Download(url, file, modifiedTime)
				info, err := os.Stat(file.Name())
				Ω(err).ShouldNot(HaveOccurred())
				Ω(info.Size()).Should(Equal(int64(0)))
			})
		})
	})
})
