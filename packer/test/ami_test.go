package test

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	mmsemver "github.com/Masterminds/semver"
	terratest_aws "github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/packer"
	"github.com/gruntwork-io/terratest/modules/ssh"
	"github.com/gruntwork-io/terratest/modules/terraform"
)

// commandline flags
var ami = flag.String("ami", "", "AMI Id to test instead of building a new one.")
var ver = flag.String("ver", "", "TFE version used in the name of the airgap package.")
var url = flag.String("url", "", "TFE valid download URL.")
var replicatedVer = flag.String("replicated-ver", "", "Replicated version to use. Optional.")

// PackerAmiTest tests the AMI built by the packer template
//
// - builds an AMI in AWS with packer in case the AMI Id is not passed via commandline argument
// - provisions an EC2 instance based on this AMI using terraform with configuration from ./terraform_fixture
// - tests the EC2 instance using ssh
// - cleanup
func TestPackerAmi(t *testing.T) {

	var amiID string
	var tfeVer string
	var tfeURL string
	var rVer string

	// parse flags
	flag.Parse()
	amiID = *ami
	tfeVer = *ver
	tfeURL = *url
	rVer = *replicatedVer

	// Ensure that one of the url or ami flags were passed.
	if tfeURL == "" && amiID == "" {
		t.Fatal("both, url and ami, flags were not set.")
	}

	// check if AWS_REGION env var is set and fail if not
	awsRegion := os.Getenv("AWS_REGION")
	if awsRegion == "" {
		t.Fatalf("AWS_REGION is not set.")
	}
	var isBuildAmi = false
	if amiID == "" {
		isBuildAmi = true
		// BUILD the AMI with PACKER

		// Occasionally, a Packer build may fail due to intermittent issues (e.g., brief network outage or EC2 issue). We try
		// to make our tests resilient to that by specifying those known common errors here and telling our builds to retry if
		// they hit those errors.
		var DefaultRetryablePackerErrors = map[string]string{
			"Script disconnected unexpectedly":                                                 "Occasionally, Packer seems to lose connectivity to AWS, perhaps due to a brief network outage",
			"can not open /var/lib/apt/lists/archive.ubuntu.com_ubuntu_dists_xenial_InRelease": "Occasionally, apt-get fails on ubuntu to update the cache",
		}
		var DefaultTimeBetweenPackerRetries = 15 * time.Second
		var DefaultMaxPackerRetries = 3
		po := &packer.Options{
			Template: "../template.json",
			Vars: map[string]string{
				"base_ami_id": terratest_aws.GetMostRecentAmiId(t, awsRegion,
					terratest_aws.CanonicalAccountId, map[string][]string{
						"name": []string{"ubuntu/images/hvm-ssd/ubuntu-bionic-18.04-amd64-server-*"},
					}),
				"tfev4_version":      tfeVer,
				"tfev4_url":          tfeURL,
				"replicated_version": rVer,
				"aws_region":         awsRegion,
				"ami_tag_owner":      "packer-test-tfev4-aws-install",
				"ami_tag_project":    "packer-test-tfev4-aws-install",
			},
			// Configure retries for intermittent errors
			RetryableErrors:    DefaultRetryablePackerErrors,
			TimeBetweenRetries: DefaultTimeBetweenPackerRetries,
			MaxRetries:         DefaultMaxPackerRetries,
		}
		var err error
		amiID, err = packer.BuildArtifactE(t, po)
		if err != nil {
			t.Fatalf("packer build failed: %v", err)
		}

		defer terratest_aws.DeleteAmiAndAllSnapshots(t, awsRegion, amiID)
	}
	// RUN TERRAFORM to build an EC instance from the AMI

	to := &terraform.Options{
		Vars: map[string]interface{}{
			"ami_id":     amiID,
			"aws_region": awsRegion,
		},
		TerraformDir: "./terraform_fixture/",
	}

	_, err := terraform.InitAndApplyE(t, to)
	defer terraform.Destroy(t, to)

	if err != nil {
		t.Fatalf("terraform failed to provision AWS instance: %v", err)
	}

	// confugire SSH

	kp := &ssh.KeyPair{
		PrivateKey: terraform.Output(t, to, "private_key"),
		PublicKey:  terraform.Output(t, to, "public_key"),
	}

	h := ssh.Host{
		Hostname:    terraform.Output(t, to, "public_dns"),
		SshKeyPair:  kp,
		SshUserName: "ubuntu",
	}

	// wait for ssh to become available
	maxRetry, i := 10, 0
	for {
		err = ssh.CheckSshConnectionE(t, h)
		if err == nil {
			break
		} else if i >= maxRetry {
			t.Fatalf("ssh connection error: %v", err)
		}
		log.Printf("waiting for ssh to become available...")
		time.Sleep(5 * time.Second)
		i++
	}

	// RUN TESTS

	// Test if needed files are in place
	files := []string{
		"/opt/tfe-installer/terraform-enterprise.airgap",
		"/opt/tfe-installer/tfev4.rli",
		"/opt/tfe-installer/cert.pem",
		"/opt/tfe-installer/privkey.pem",
		"/opt/tfe-installer/install.sh",
	}

	for _, file := range files {
		err = checkFileExists(t, h, file)
		if err != nil {
			t.Errorf("check failed for file %q : %v", file, err)
		}
		t.Logf("success, file %q found.", file)
	}

	// Test if Docker CE is installed and running
	out, err := ssh.CheckSshCommandE(t, h, "sudo docker version -f {{.Server.Version}}")
	if err != nil {
		t.Errorf("success fail docker check: %v: %s", err, out)
	} else {
		t.Logf("docker found, version: %s", out)
	}

	// Test if Docker CE is correct version
	dockerVer, err := mmsemver.NewVersion(strings.TrimSpace(strings.TrimSuffix(out, "\n")))
	if err != nil {
		t.Errorf("failed to parse docker version %q: %v", out, err)
	}

	var minVer, maxVer *mmsemver.Constraints
	minVer, _ = mmsemver.NewConstraint(">= 17.06.2") // ignoring error as constraint is hardcoded
	maxVer, _ = mmsemver.NewConstraint("<= 19.03.8") // ignoring error as constraint is hardcoded

	if !(minVer.Check(dockerVer) && maxVer.Check(dockerVer)) {
		t.Errorf("filed docker version check version %q is not between %q and %q",
			dockerVer.Original(), minVer.String(), maxVer.String())
	}

	// Test if specific commands are installed
	var cmds = []string{"aws", "htop", "ctop"}
	for _, cmd := range cmds {
		err := checkCommandExists(t, h, cmd)
		if err != nil {
			t.Errorf("check filed for command %q : %v", cmd, err)
		}
		t.Logf("success command %q is present.", cmd)
	}

	// Test if periodic apt upgrades are disabled
	out, err = ssh.CheckSshCommandE(t, h, "cat /etc/apt/apt.conf.d/10periodic | grep 'APT::Periodic::Enable'")
	if err != nil {
		t.Errorf("failed running \"cat /etc/apt/apt.conf.d/10periodic | grep 'APT::Periodic::Enable'\": %v", err)
	} else if out != "APT::Periodic::Enable \"0\";\n" {
		t.Errorf("periodic apt updates are not disabled, want:'APT::Periodic::Enable \"0\";', got: %q", out)
	} else {
		t.Log("success. periodic apt updates are disabled.")
	}

	// Test AMI tags if image was built as part of the test
	if isBuildAmi {

		var wantTags = map[string]string{
			"owner":              "packer-test-tfev4-aws-install",
			"project":            "packer-test-tfev4-aws-install",
			"replicated_version": rVer,
			"tfe_version":        tfeVer,
		}

		var gotTags map[string]string
		gotTags, err = terraform.OutputMapE(t, to, "ami_tags")

		if err != nil {
			t.Errorf("error getting terraform output 'ami_tags': %v", err)
		} else {
			for k, v := range wantTags {
				gotV, ok := gotTags[k]
				if !ok {
					t.Errorf("tag %q not present in ami tags", k)
					continue
				}
				if v != gotV {
					t.Errorf("unexpected value for tag %q, got: %q, want: %q", k, gotV, v)
				}
			}
		}

	}

}

// checkFileExists checks if a file is presnet on the ssh host
func checkFileExists(t *testing.T, h ssh.Host, file string) error {
	out, err := ssh.CheckSshCommandE(t, h, fmt.Sprintf("stat %q", file))
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// checkCommandExists checks if a command is presnet on the ssh host and added to PATH
func checkCommandExists(t *testing.T, h ssh.Host, cmd string) error {
	out, err := ssh.CheckSshCommandE(t, h, fmt.Sprintf("command -v %q", cmd))
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}
