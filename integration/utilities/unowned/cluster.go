package unowned

import (
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	cfn "github.com/aws/aws-sdk-go/service/cloudformation"
	awseks "github.com/aws/aws-sdk-go/service/eks"
	. "github.com/onsi/gomega"
	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"

	"github.com/weaveworks/eksctl/pkg/eks"
)

type Cluster struct {
	Cfg              *api.ClusterConfig
	Ctl              api.ClusterProvider
	ClusterName      string
	ClusterStackName string
	PublicSubnets    []string
	PrivateSubnets   []string
	ClusterRoleARN   string
	NodeRoleARN      string
}

var timeoutDuration = time.Minute * 30

func NewCluster(cfg *api.ClusterConfig) *Cluster {
	stackName := fmt.Sprintf("eksctl-%s", cfg.Metadata.Name)

	clusterProvider, err := eks.New(&api.ProviderConfig{Region: cfg.Metadata.Region}, cfg)
	Expect(err).NotTo(HaveOccurred())
	ctl := clusterProvider.Provider
	publicSubnets, privateSubnets, clusterRoleARN, nodeRoleARN := createVPCAndRole(stackName, ctl)

	uc := &Cluster{
		Cfg:              cfg,
		Ctl:              ctl,
		ClusterStackName: stackName,
		ClusterName:      cfg.Metadata.Name,
		PublicSubnets:    publicSubnets,
		PrivateSubnets:   privateSubnets,
		ClusterRoleARN:   clusterRoleARN,
		NodeRoleARN:      nodeRoleARN,
	}

	uc.createCluster()
	return uc
}

func (uc *Cluster) DeleteStack() {
	deleteStackInput := &cfn.DeleteStackInput{
		StackName: &uc.ClusterStackName,
	}

	_, err := uc.Ctl.CloudFormation().DeleteStack(deleteStackInput)
	Expect(err).NotTo(HaveOccurred())
}

func (uc *Cluster) createCluster() {
	_, err := uc.Ctl.EKS().CreateCluster(&awseks.CreateClusterInput{
		Name: &uc.ClusterName,
		ResourcesVpcConfig: &awseks.VpcConfigRequest{
			SubnetIds: aws.StringSlice(append(uc.PublicSubnets, uc.PrivateSubnets...)),
		},
		RoleArn: &uc.ClusterRoleARN,
		Version: &uc.Cfg.Metadata.Version,
	})
	Expect(err).NotTo(HaveOccurred())
	Eventually(func() string {
		out, err := uc.Ctl.EKS().DescribeCluster(&awseks.DescribeClusterInput{
			Name: &uc.ClusterName,
		})
		Expect(err).NotTo(HaveOccurred())
		return *out.Cluster.Status
	}, timeoutDuration, time.Second*30).Should(Equal("ACTIVE"))
}

func (uc Cluster) CreateNodegroups(names ...string) {
	for _, name := range names {
		_, err := uc.Ctl.EKS().CreateNodegroup(&awseks.CreateNodegroupInput{
			NodegroupName: &name,
			ClusterName:   &uc.ClusterName,
			NodeRole:      &uc.NodeRoleARN,
			Subnets:       aws.StringSlice(uc.PublicSubnets),
			ScalingConfig: &awseks.NodegroupScalingConfig{
				MaxSize:     aws.Int64(1),
				DesiredSize: aws.Int64(1),
				MinSize:     aws.Int64(1),
			},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	for _, name := range names {
		Eventually(func() string {
			out, err := uc.Ctl.EKS().DescribeNodegroup(&awseks.DescribeNodegroupInput{
				ClusterName:   &uc.ClusterName,
				NodegroupName: &name,
			})
			Expect(err).NotTo(HaveOccurred())
			return *out.Nodegroup.Status
		}, timeoutDuration, time.Second*30).Should(Equal("ACTIVE"))
	}
}

func createVPCAndRole(stackName string, ctl api.ClusterProvider) ([]string, []string, string, string) {
	templateBody, err := ioutil.ReadFile("../../utilities/unowned/cf-template.yaml")
	Expect(err).NotTo(HaveOccurred())
	createStackInput := &cfn.CreateStackInput{
		StackName: &stackName,
	}
	createStackInput.SetTemplateBody(string(templateBody))
	createStackInput.SetCapabilities(aws.StringSlice([]string{cfn.CapabilityCapabilityIam}))
	createStackInput.SetCapabilities(aws.StringSlice([]string{cfn.CapabilityCapabilityNamedIam}))

	_, err = ctl.CloudFormation().CreateStack(createStackInput)
	Expect(err).NotTo(HaveOccurred())

	var describeStackOut *cfn.DescribeStacksOutput
	Eventually(func() string {
		describeStackOut, err = ctl.CloudFormation().DescribeStacks(&cfn.DescribeStacksInput{
			StackName: &stackName,
		})
		Expect(err).NotTo(HaveOccurred())
		return *describeStackOut.Stacks[0].StackStatus
	}, time.Minute*10, time.Second*15).Should(Equal(cfn.StackStatusCreateComplete))

	var clusterRoleARN, nodeRoleARN string
	var publicSubnets, privateSubnets []string
	for _, output := range describeStackOut.Stacks[0].Outputs {
		switch *output.OutputKey {
		case "ClusterRoleARN":
			clusterRoleARN = *output.OutputValue
		case "NodeRoleARN":
			nodeRoleARN = *output.OutputValue
		case "PublicSubnetIds":
			publicSubnets = strings.Split(*output.OutputValue, ",")
		case "PrivateSubnetIds":
			privateSubnets = strings.Split(*output.OutputValue, ",")
		}
	}

	return publicSubnets, privateSubnets, clusterRoleARN, nodeRoleARN
}
