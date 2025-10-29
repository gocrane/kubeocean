// Copyright 2025 The Kubeocean Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=lc
// +kubebuilder:subresource:status

// LogConfig is the Schema for the logconfigs API with correct group and version
type LogConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LogConfigSpec   `json:"spec,omitempty"`
	Status LogConfigStatus `json:"status,omitempty"`
}

// LogConfigSpec defines the desired state of LogConfig
type LogConfigSpec struct {
	// CLSDetail contains CLS-specific configuration
	// +optional
	CLSDetail *CLSDetail `json:"clsDetail,omitempty"`

	// InputDetail defines the input source configuration
	// +optional
	InputDetail *InputDetail `json:"inputDetail,omitempty"`

	// KafkaDetail contains Kafka-specific configuration
	// +optional
	KafkaDetail *KafkaDetail `json:"kafkaDetail,omitempty"`
}

// CLSDetail contains CLS-specific configuration
type CLSDetail struct {
	// AdvancedConfig contains advanced CLS configuration
	// +optional
	AdvancedConfig *AdvancedConfig `json:"advancedConfig,omitempty"`

	// AutoIndex configuration
	// +optional
	AutoIndex *string `json:"autoIndex,omitempty"`

	// AutoSplit configuration
	// +optional
	AutoSplit *string `json:"autoSplit,omitempty"`

	// ExcludePaths defines paths to exclude
	// +optional
	ExcludePaths []ExcludePath `json:"excludePaths,omitempty"`

	// ExtractRule defines log extraction rules
	// +optional
	ExtractRule *ExtractRule `json:"extractRule,omitempty"`

	// FullTextIndex configuration
	// +optional
	FullTextIndex *FullTextIndex `json:"fullTextIndex,omitempty"`

	// HotPeriod defines the hot period in days
	// +optional
	HotPeriod *int32 `json:"hotPeriod,omitempty"`

	// IndexStatus defines the index status
	// +optional
	IndexStatus *string `json:"indexStatus,omitempty"`

	// Indexs defines the index configuration
	// +optional
	Indexs []Index `json:"indexs,omitempty"`

	// LogFormat defines the log format
	// +optional
	LogFormat *string `json:"logFormat,omitempty"`

	// LogType defines the log type
	// +optional
	LogType *string `json:"logType,omitempty"`

	// LogsetId defines the logset ID
	// +optional
	LogsetId *string `json:"logsetId,omitempty"`

	// LogsetName defines the logset name
	// +optional
	LogsetName *string `json:"logsetName,omitempty"`

	// MaxSplitPartitions defines maximum split partitions
	// +optional
	MaxSplitPartitions *int32 `json:"maxSplitPartitions,omitempty"`

	// PartitionCount defines the partition count
	// +optional
	PartitionCount *int32 `json:"partitionCount,omitempty"`

	// Period defines the period in days
	// +optional
	Period *int32 `json:"period,omitempty"`

	// Region defines the CLS region
	// +optional
	Region *string `json:"region,omitempty"`

	// StorageType defines the storage type
	// +optional
	StorageType *string `json:"storageType,omitempty"`

	// Tags defines the tags
	// +optional
	Tags []Tag `json:"tags,omitempty"`

	// TopicId defines the topic ID
	// +optional
	TopicId *string `json:"topicId,omitempty"`

	// TopicName defines the topic name
	// +optional
	TopicName *string `json:"topicName,omitempty"`

	// UserDefineRule defines user-defined rules
	// +optional
	UserDefineRule *string `json:"userDefineRule,omitempty"`
}

// AdvancedConfig contains advanced CLS configuration
type AdvancedConfig struct {
	// CLSAgentFileTimeout defines file timeout
	// +optional
	CLSAgentFileTimeout *int32 `json:"ClsAgentFileTimeout,omitempty"`

	// CLSAgentMaxDepth defines max depth
	// +optional
	CLSAgentMaxDepth *int32 `json:"ClsAgentMaxDepth,omitempty"`

	// CLSAgentParseFailMerge defines parse fail merge behavior
	// +optional
	CLSAgentParseFailMerge *bool `json:"ClsAgentParseFailMerge,omitempty"`
}

// ExcludePath defines a path to exclude
type ExcludePath struct {
	// ExcludeType defines the exclude type
	// +optional
	ExcludeType *string `json:"excludeType,omitempty"`

	// Value defines the exclude value
	// +optional
	Value *string `json:"value,omitempty"`
}

// ExtractRule defines log extraction rules
type ExtractRule struct {
	// AdvancedFilters defines advanced filters
	// +optional
	AdvancedFilters []AdvancedFilter `json:"advancedFilters,omitempty"`

	// Backtracking configuration
	// +optional
	Backtracking *string `json:"backtracking,omitempty"`

	// BeginningRegex defines the beginning regex
	// +optional
	BeginningRegex *string `json:"beginningRegex,omitempty"`

	// Delimiter defines the delimiter
	// +optional
	Delimiter *string `json:"delimiter,omitempty"`

	// FilterKeys defines filter keys
	// +optional
	FilterKeys []string `json:"filterKeys,omitempty"`

	// FilterRegex defines filter regex patterns
	// +optional
	FilterRegex []string `json:"filterRegex,omitempty"`

	// IsGBK defines if using GBK encoding
	// +optional
	IsGBK *string `json:"isGBK,omitempty"`

	// JsonStandard defines JSON standard
	// +optional
	JsonStandard *string `json:"jsonStandard,omitempty"`

	// Keys defines the keys
	// +optional
	Keys []string `json:"keys,omitempty"`

	// LogRegex defines the log regex
	// +optional
	LogRegex *string `json:"logRegex,omitempty"`

	// RawLogKey defines the raw log key
	// +optional
	RawLogKey *string `json:"rawLogKey,omitempty"`

	// TimeFormat defines the time format
	// +optional
	TimeFormat *string `json:"timeFormat,omitempty"`

	// TimeKey defines the time key
	// +optional
	TimeKey *string `json:"timeKey,omitempty"`

	// UnMatchUpload defines unmatch upload behavior
	// +optional
	UnMatchUpload *string `json:"unMatchUpload,omitempty"`

	// UnMatchedKey defines the unmatched key
	// +optional
	UnMatchedKey *string `json:"unMatchedKey,omitempty"`
}

// AdvancedFilter defines an advanced filter
type AdvancedFilter struct {
	// Key defines the filter key
	// +optional
	Key *string `json:"key,omitempty"`

	// Rule defines the filter rule
	// +optional
	Rule *int32 `json:"rule,omitempty"`

	// Value defines the filter value
	// +optional
	Value *string `json:"value,omitempty"`
}

// FullTextIndex defines full text index configuration
type FullTextIndex struct {
	// CaseSensitive defines if case sensitive
	CaseSensitive bool `json:"caseSensitive"`

	// ContainZH defines if contains Chinese
	ContainZH bool `json:"containZH"`

	// Status defines the index status
	Status string `json:"status"`

	// Tokenizer defines the tokenizer
	Tokenizer string `json:"tokenizer"`
}

// Index defines index configuration
type Index struct {
	// ContainZH defines if contains Chinese
	// +optional
	ContainZH *bool `json:"containZH,omitempty"`

	// IndexName defines the index name
	// +optional
	IndexName *string `json:"indexName,omitempty"`

	// IndexType defines the index type
	// +optional
	IndexType *string `json:"indexType,omitempty"`

	// SqlFlag defines SQL flag
	// +optional
	SqlFlag *bool `json:"sqlFlag,omitempty"`

	// Tokenizer defines the tokenizer
	// +optional
	Tokenizer *string `json:"tokenizer,omitempty"`
}

// Tag defines a tag
type Tag struct {
	// Key defines the tag key
	// +optional
	Key *string `json:"key,omitempty"`

	// Value defines the tag value
	// +optional
	Value *string `json:"value,omitempty"`
}

// InputDetail defines the input source configuration
type InputDetail struct {
	// Type defines the input type
	// +kubebuilder:validation:Enum=container_stdout;container_file;host_file
	// +optional
	Type *string `json:"type,omitempty"`

	// ContainerFile defines container file input
	// +optional
	ContainerFile *ContainerFile `json:"containerFile,omitempty"`

	// ContainerStdout defines container stdout input
	// +optional
	ContainerStdout *ContainerStdout `json:"containerStdout,omitempty"`

	// HostFile defines host file input
	// +optional
	HostFile *HostFile `json:"hostFile,omitempty"`
}

// ContainerFile defines container file input configuration
type ContainerFile struct {
	// Container defines the container name
	// +optional
	Container *string `json:"container,omitempty"`

	// ContainerOperator defines the container operator
	// +optional
	ContainerOperator *string `json:"containerOperator,omitempty"`

	// CustomLabels defines custom labels
	// +optional
	CustomLabels map[string]string `json:"customLabels,omitempty"`

	// ExcludeLabels defines labels to exclude
	// +optional
	ExcludeLabels map[string]string `json:"excludeLabels,omitempty"`

	// ExcludeNamespace defines namespace to exclude
	// +optional
	ExcludeNamespace *string `json:"excludeNamespace,omitempty"`

	// FilePaths defines file paths
	// +optional
	FilePaths []FilePath `json:"filePaths,omitempty"`

	// FilePattern defines file pattern
	// +optional
	FilePattern *string `json:"filePattern,omitempty"`

	// IncludeLabels defines labels to include
	// +optional
	IncludeLabels map[string]string `json:"includeLabels,omitempty"`

	// LogPath defines the log path
	// +optional
	LogPath *string `json:"logPath,omitempty"`

	// MetadataContainer defines metadata containers
	// +optional
	MetadataContainer []string `json:"metadataContainer,omitempty"`

	// MetadataLabels defines metadata labels
	// +optional
	MetadataLabels []string `json:"metadataLabels,omitempty"`

	// Namespace defines the namespace
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// NsLabelSelector defines namespace label selector
	// +optional
	NsLabelSelector *string `json:"nsLabelSelector,omitempty"`

	// Workload defines the workload
	// +optional
	Workload *Workload `json:"workload,omitempty"`
}

// ContainerStdout defines container stdout input configuration
type ContainerStdout struct {
	// AllContainers defines if all containers
	// +optional
	AllContainers *bool `json:"allContainers,omitempty"`

	// Container defines the container name
	// +optional
	Container *string `json:"container,omitempty"`

	// ContainerOperator defines the container operator
	// +optional
	ContainerOperator *string `json:"containerOperator,omitempty"`

	// CustomLabels defines custom labels
	// +optional
	CustomLabels map[string]string `json:"customLabels,omitempty"`

	// ExcludeLabels defines labels to exclude
	// +optional
	ExcludeLabels map[string]string `json:"excludeLabels,omitempty"`

	// ExcludeNamespace defines namespace to exclude
	// +optional
	ExcludeNamespace *string `json:"excludeNamespace,omitempty"`

	// IncludeLabels defines labels to include
	// +optional
	IncludeLabels map[string]string `json:"includeLabels,omitempty"`

	// MetadataContainer defines metadata containers
	// +optional
	MetadataContainer []string `json:"metadataContainer,omitempty"`

	// MetadataLabels defines metadata labels
	// +optional
	MetadataLabels []string `json:"metadataLabels,omitempty"`

	// Namespace defines the namespace
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// NsLabelSelector defines namespace label selector
	// +optional
	NsLabelSelector *string `json:"nsLabelSelector,omitempty"`

	// Workloads defines the workloads
	// +optional
	Workloads []WorkloadSelector `json:"workloads,omitempty"`
}

// HostFile defines host file input configuration
type HostFile struct {
	// CustomLabels defines custom labels
	// +optional
	CustomLabels map[string]string `json:"customLabels,omitempty"`

	// FilePaths defines file paths
	// +optional
	FilePaths []FilePath `json:"filePaths,omitempty"`

	// FilePattern defines file pattern
	// +optional
	FilePattern *string `json:"filePattern,omitempty"`

	// LogPath defines the log path
	// +optional
	LogPath *string `json:"logPath,omitempty"`
}

// FilePath defines a file path
type FilePath struct {
	// File defines the file name
	// +optional
	File *string `json:"file,omitempty"`

	// Path defines the path
	// +optional
	Path *string `json:"path,omitempty"`
}

// Workload defines a workload
type Workload struct {
	// Kind defines the workload kind
	// +optional
	Kind *string `json:"kind,omitempty"`

	// Name defines the workload name
	// +optional
	Name *string `json:"name,omitempty"`
}

// WorkloadSelector defines a workload selector
type WorkloadSelector struct {
	// Container defines the container name
	// +optional
	Container *string `json:"container,omitempty"`

	// ContainerOperator defines the container operator
	// +optional
	ContainerOperator *string `json:"containerOperator,omitempty"`

	// Kind defines the workload kind
	// +kubebuilder:validation:Enum=deployment;daemonset;statefulset;job;cronjob
	// +optional
	Kind *string `json:"kind,omitempty"`

	// Name defines the workload name
	// +optional
	Name *string `json:"name,omitempty"`

	// Namespace defines the namespace
	// +optional
	Namespace *string `json:"namespace,omitempty"`
}

// KafkaDetail defines Kafka output configuration
type KafkaDetail struct {
	// Brokers defines Kafka brokers
	// +optional
	Brokers *string `json:"brokers,omitempty"`

	// ExtractRule defines extraction rules for Kafka
	// +optional
	ExtractRule *KafkaExtractRule `json:"extractRule,omitempty"`

	// InstanceId defines the Kafka instance ID
	// +optional
	InstanceId *string `json:"instanceId,omitempty"`

	// KafkaType defines the Kafka type
	// +optional
	KafkaType *string `json:"kafkaType,omitempty"`

	// LogType defines the log type for Kafka
	// +optional
	LogType *string `json:"logType,omitempty"`

	// MessageKey defines the message key configuration
	// +optional
	MessageKey *MessageKey `json:"messageKey,omitempty"`

	// Metadata defines metadata configuration
	// +optional
	Metadata *KafkaMetadata `json:"metadata,omitempty"`

	// OutputType defines the output type
	// +optional
	OutputType *string `json:"outputType,omitempty"`

	// RdKafka defines additional Kafka configuration
	// +optional
	RdKafka map[string]string `json:"rdKafka,omitempty"`

	// TimestampFormat defines the timestamp format
	// +optional
	TimestampFormat *string `json:"timestampFormat,omitempty"`

	// TimestampKey defines the timestamp key
	// +optional
	TimestampKey *string `json:"timestampKey,omitempty"`

	// Topic defines the Kafka topic
	// +optional
	Topic *string `json:"topic,omitempty"`
}

// KafkaExtractRule defines extraction rules for Kafka
type KafkaExtractRule struct {
	// BeginningRegex defines the beginning regex
	// +optional
	BeginningRegex *string `json:"beginningRegex,omitempty"`
}

// MessageKey defines message key configuration
type MessageKey struct {
	// Value defines the static value
	// +optional
	Value *string `json:"value,omitempty"`

	// ValueFrom defines the value source
	// +optional
	ValueFrom *ValueFrom `json:"valueFrom,omitempty"`
}

// ValueFrom defines value source
type ValueFrom struct {
	// FieldRef defines field reference
	// +optional
	FieldRef *FieldRef `json:"fieldRef,omitempty"`
}

// FieldRef defines field reference
type FieldRef struct {
	// FieldPath defines the field path
	// +optional
	FieldPath *string `json:"fieldPath,omitempty"`
}

// KafkaMetadata defines Kafka metadata configuration
type KafkaMetadata struct {
	// FormatType defines the format type
	// +optional
	FormatType *string `json:"formatType,omitempty"`
}

// LogConfigStatus defines the observed state of LogConfig
type LogConfigStatus struct {
	// Code defines the status code
	// +optional
	Code *string `json:"code,omitempty"`

	// Reason defines the status reason
	// +optional
	Reason *string `json:"reason,omitempty"`

	// Status defines the overall status
	// +optional
	Status *string `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LogConfigList contains a list of LogConfig
type LogConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LogConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LogConfig{}, &LogConfigList{})
}
