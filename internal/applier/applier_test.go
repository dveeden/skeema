package applier

import (
	"fmt"
	"os"
	"testing"

	"github.com/skeema/skeema/internal/tengo"
	"github.com/skeema/skeema/internal/util"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	// Suppress packet error output when attempting to connect to a Dockerized
	// mysql-server which is still starting up
	tengo.UseFilteredDriverLogger()

	os.Exit(m.Run())
}

func TestResultMerge(t *testing.T) {
	r := Result{
		Differences:      false,
		SkipCount:        1,
		UnsupportedCount: 0,
	}
	other := Result{
		Differences:      true,
		SkipCount:        3,
		UnsupportedCount: 5,
	}
	expectSum := Result{
		Differences:      true,
		SkipCount:        4,
		UnsupportedCount: 5,
	}
	r.Merge(other)
	if r != expectSum {
		t.Errorf("Unexpected result from SumResults: %+v", r)
	}
}

func TestIntegration(t *testing.T) {
	images := tengo.SkeemaTestImages(t)
	suite := &ApplierIntegrationSuite{}
	tengo.RunSuite(suite, t, images)
}

type ApplierIntegrationSuite struct {
	d []*tengo.DockerizedInstance
}

func (s *ApplierIntegrationSuite) Setup(backend string) error {
	var g errgroup.Group
	s.d = make([]*tengo.DockerizedInstance, 2)
	for n := range s.d {
		n := n
		g.Go(func() error {
			var err error
			containerName := fmt.Sprintf("skeema-test-%s", tengo.ContainerNameForImage(backend))
			if n > 0 {
				containerName = fmt.Sprintf("%s-%d", containerName, n+1)
			}
			s.d[n], err = tengo.GetOrCreateDockerizedInstance(tengo.DockerizedInstanceOptions{
				Name:         containerName,
				Image:        backend,
				RootPassword: "fakepw",
				CommandArgs:  []string{"--skip-log-bin"}, // override MySQL 8 default of enabling binlog
			})
			return err
		})
	}
	return g.Wait()
}

func (s *ApplierIntegrationSuite) Teardown(backend string) error {
	var g errgroup.Group
	for n := range s.d {
		n := n
		g.Go(func() error {
			// Only keep the first container; destroy any additional, since the other
			// subpackages only use 1 test container
			if n == 0 {
				return s.d[n].Stop()
			}
			return s.d[n].Destroy()
		})
	}
	err := g.Wait()
	util.FlushInstanceCache()
	return err
}

func (s *ApplierIntegrationSuite) BeforeTest(backend string) error {
	var g errgroup.Group
	for n := range s.d {
		n := n
		g.Go(func() error {
			return s.d[n].NukeData()
		})
	}
	return g.Wait()
}
