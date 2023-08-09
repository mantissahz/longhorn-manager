// Code generated by protoc-gen-go. DO NOT EDIT.
// source: github.com/longhorn/longhorn-instance-manager/pkg/imrpc/disk.proto

package imrpc

import (
	context "context"
	fmt "fmt"
	proto "github.com/golang/protobuf/proto"
	empty "github.com/golang/protobuf/ptypes/empty"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion3 // please upgrade the proto package

type DiskType int32

const (
	DiskType_filesystem DiskType = 0
	DiskType_block      DiskType = 1
)

var DiskType_name = map[int32]string{
	0: "filesystem",
	1: "block",
}

var DiskType_value = map[string]int32{
	"filesystem": 0,
	"block":      1,
}

func (x DiskType) String() string {
	return proto.EnumName(DiskType_name, int32(x))
}

func (DiskType) EnumDescriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{0}
}

type Disk struct {
	Id                   string   `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	Uuid                 string   `protobuf:"bytes,2,opt,name=uuid,proto3" json:"uuid,omitempty"`
	Path                 string   `protobuf:"bytes,3,opt,name=path,proto3" json:"path,omitempty"`
	Type                 string   `protobuf:"bytes,4,opt,name=type,proto3" json:"type,omitempty"`
	TotalSize            int64    `protobuf:"varint,5,opt,name=total_size,json=totalSize,proto3" json:"total_size,omitempty"`
	FreeSize             int64    `protobuf:"varint,6,opt,name=free_size,json=freeSize,proto3" json:"free_size,omitempty"`
	TotalBlocks          int64    `protobuf:"varint,7,opt,name=total_blocks,json=totalBlocks,proto3" json:"total_blocks,omitempty"`
	FreeBlocks           int64    `protobuf:"varint,8,opt,name=free_blocks,json=freeBlocks,proto3" json:"free_blocks,omitempty"`
	BlockSize            int64    `protobuf:"varint,9,opt,name=block_size,json=blockSize,proto3" json:"block_size,omitempty"`
	ClusterSize          int64    `protobuf:"varint,10,opt,name=cluster_size,json=clusterSize,proto3" json:"cluster_size,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *Disk) Reset()         { *m = Disk{} }
func (m *Disk) String() string { return proto.CompactTextString(m) }
func (*Disk) ProtoMessage()    {}
func (*Disk) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{0}
}

func (m *Disk) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_Disk.Unmarshal(m, b)
}
func (m *Disk) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_Disk.Marshal(b, m, deterministic)
}
func (m *Disk) XXX_Merge(src proto.Message) {
	xxx_messageInfo_Disk.Merge(m, src)
}
func (m *Disk) XXX_Size() int {
	return xxx_messageInfo_Disk.Size(m)
}
func (m *Disk) XXX_DiscardUnknown() {
	xxx_messageInfo_Disk.DiscardUnknown(m)
}

var xxx_messageInfo_Disk proto.InternalMessageInfo

func (m *Disk) GetId() string {
	if m != nil {
		return m.Id
	}
	return ""
}

func (m *Disk) GetUuid() string {
	if m != nil {
		return m.Uuid
	}
	return ""
}

func (m *Disk) GetPath() string {
	if m != nil {
		return m.Path
	}
	return ""
}

func (m *Disk) GetType() string {
	if m != nil {
		return m.Type
	}
	return ""
}

func (m *Disk) GetTotalSize() int64 {
	if m != nil {
		return m.TotalSize
	}
	return 0
}

func (m *Disk) GetFreeSize() int64 {
	if m != nil {
		return m.FreeSize
	}
	return 0
}

func (m *Disk) GetTotalBlocks() int64 {
	if m != nil {
		return m.TotalBlocks
	}
	return 0
}

func (m *Disk) GetFreeBlocks() int64 {
	if m != nil {
		return m.FreeBlocks
	}
	return 0
}

func (m *Disk) GetBlockSize() int64 {
	if m != nil {
		return m.BlockSize
	}
	return 0
}

func (m *Disk) GetClusterSize() int64 {
	if m != nil {
		return m.ClusterSize
	}
	return 0
}

type ReplicaInstance struct {
	Name                 string   `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Uuid                 string   `protobuf:"bytes,2,opt,name=uuid,proto3" json:"uuid,omitempty"`
	DiskName             string   `protobuf:"bytes,3,opt,name=disk_name,json=diskName,proto3" json:"disk_name,omitempty"`
	DiskUuid             string   `protobuf:"bytes,4,opt,name=disk_uuid,json=diskUuid,proto3" json:"disk_uuid,omitempty"`
	SpecSize             uint64   `protobuf:"varint,5,opt,name=spec_size,json=specSize,proto3" json:"spec_size,omitempty"`
	ActualSize           uint64   `protobuf:"varint,6,opt,name=actual_size,json=actualSize,proto3" json:"actual_size,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *ReplicaInstance) Reset()         { *m = ReplicaInstance{} }
func (m *ReplicaInstance) String() string { return proto.CompactTextString(m) }
func (*ReplicaInstance) ProtoMessage()    {}
func (*ReplicaInstance) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{1}
}

func (m *ReplicaInstance) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_ReplicaInstance.Unmarshal(m, b)
}
func (m *ReplicaInstance) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_ReplicaInstance.Marshal(b, m, deterministic)
}
func (m *ReplicaInstance) XXX_Merge(src proto.Message) {
	xxx_messageInfo_ReplicaInstance.Merge(m, src)
}
func (m *ReplicaInstance) XXX_Size() int {
	return xxx_messageInfo_ReplicaInstance.Size(m)
}
func (m *ReplicaInstance) XXX_DiscardUnknown() {
	xxx_messageInfo_ReplicaInstance.DiscardUnknown(m)
}

var xxx_messageInfo_ReplicaInstance proto.InternalMessageInfo

func (m *ReplicaInstance) GetName() string {
	if m != nil {
		return m.Name
	}
	return ""
}

func (m *ReplicaInstance) GetUuid() string {
	if m != nil {
		return m.Uuid
	}
	return ""
}

func (m *ReplicaInstance) GetDiskName() string {
	if m != nil {
		return m.DiskName
	}
	return ""
}

func (m *ReplicaInstance) GetDiskUuid() string {
	if m != nil {
		return m.DiskUuid
	}
	return ""
}

func (m *ReplicaInstance) GetSpecSize() uint64 {
	if m != nil {
		return m.SpecSize
	}
	return 0
}

func (m *ReplicaInstance) GetActualSize() uint64 {
	if m != nil {
		return m.ActualSize
	}
	return 0
}

type DiskCreateRequest struct {
	DiskType             DiskType `protobuf:"varint,1,opt,name=disk_type,json=diskType,proto3,enum=imrpc.DiskType" json:"disk_type,omitempty"`
	DiskName             string   `protobuf:"bytes,2,opt,name=disk_name,json=diskName,proto3" json:"disk_name,omitempty"`
	DiskUuid             string   `protobuf:"bytes,3,opt,name=disk_uuid,json=diskUuid,proto3" json:"disk_uuid,omitempty"`
	DiskPath             string   `protobuf:"bytes,4,opt,name=disk_path,json=diskPath,proto3" json:"disk_path,omitempty"`
	BlockSize            int64    `protobuf:"varint,5,opt,name=block_size,json=blockSize,proto3" json:"block_size,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *DiskCreateRequest) Reset()         { *m = DiskCreateRequest{} }
func (m *DiskCreateRequest) String() string { return proto.CompactTextString(m) }
func (*DiskCreateRequest) ProtoMessage()    {}
func (*DiskCreateRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{2}
}

func (m *DiskCreateRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskCreateRequest.Unmarshal(m, b)
}
func (m *DiskCreateRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskCreateRequest.Marshal(b, m, deterministic)
}
func (m *DiskCreateRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskCreateRequest.Merge(m, src)
}
func (m *DiskCreateRequest) XXX_Size() int {
	return xxx_messageInfo_DiskCreateRequest.Size(m)
}
func (m *DiskCreateRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskCreateRequest.DiscardUnknown(m)
}

var xxx_messageInfo_DiskCreateRequest proto.InternalMessageInfo

func (m *DiskCreateRequest) GetDiskType() DiskType {
	if m != nil {
		return m.DiskType
	}
	return DiskType_filesystem
}

func (m *DiskCreateRequest) GetDiskName() string {
	if m != nil {
		return m.DiskName
	}
	return ""
}

func (m *DiskCreateRequest) GetDiskUuid() string {
	if m != nil {
		return m.DiskUuid
	}
	return ""
}

func (m *DiskCreateRequest) GetDiskPath() string {
	if m != nil {
		return m.DiskPath
	}
	return ""
}

func (m *DiskCreateRequest) GetBlockSize() int64 {
	if m != nil {
		return m.BlockSize
	}
	return 0
}

type DiskGetRequest struct {
	DiskType             DiskType `protobuf:"varint,1,opt,name=disk_type,json=diskType,proto3,enum=imrpc.DiskType" json:"disk_type,omitempty"`
	DiskName             string   `protobuf:"bytes,2,opt,name=disk_name,json=diskName,proto3" json:"disk_name,omitempty"`
	DiskPath             string   `protobuf:"bytes,3,opt,name=disk_path,json=diskPath,proto3" json:"disk_path,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *DiskGetRequest) Reset()         { *m = DiskGetRequest{} }
func (m *DiskGetRequest) String() string { return proto.CompactTextString(m) }
func (*DiskGetRequest) ProtoMessage()    {}
func (*DiskGetRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{3}
}

func (m *DiskGetRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskGetRequest.Unmarshal(m, b)
}
func (m *DiskGetRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskGetRequest.Marshal(b, m, deterministic)
}
func (m *DiskGetRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskGetRequest.Merge(m, src)
}
func (m *DiskGetRequest) XXX_Size() int {
	return xxx_messageInfo_DiskGetRequest.Size(m)
}
func (m *DiskGetRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskGetRequest.DiscardUnknown(m)
}

var xxx_messageInfo_DiskGetRequest proto.InternalMessageInfo

func (m *DiskGetRequest) GetDiskType() DiskType {
	if m != nil {
		return m.DiskType
	}
	return DiskType_filesystem
}

func (m *DiskGetRequest) GetDiskName() string {
	if m != nil {
		return m.DiskName
	}
	return ""
}

func (m *DiskGetRequest) GetDiskPath() string {
	if m != nil {
		return m.DiskPath
	}
	return ""
}

type DiskDeleteRequest struct {
	DiskType             DiskType `protobuf:"varint,1,opt,name=disk_type,json=diskType,proto3,enum=imrpc.DiskType" json:"disk_type,omitempty"`
	DiskName             string   `protobuf:"bytes,2,opt,name=disk_name,json=diskName,proto3" json:"disk_name,omitempty"`
	DiskUuid             string   `protobuf:"bytes,3,opt,name=disk_uuid,json=diskUuid,proto3" json:"disk_uuid,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *DiskDeleteRequest) Reset()         { *m = DiskDeleteRequest{} }
func (m *DiskDeleteRequest) String() string { return proto.CompactTextString(m) }
func (*DiskDeleteRequest) ProtoMessage()    {}
func (*DiskDeleteRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{4}
}

func (m *DiskDeleteRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskDeleteRequest.Unmarshal(m, b)
}
func (m *DiskDeleteRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskDeleteRequest.Marshal(b, m, deterministic)
}
func (m *DiskDeleteRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskDeleteRequest.Merge(m, src)
}
func (m *DiskDeleteRequest) XXX_Size() int {
	return xxx_messageInfo_DiskDeleteRequest.Size(m)
}
func (m *DiskDeleteRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskDeleteRequest.DiscardUnknown(m)
}

var xxx_messageInfo_DiskDeleteRequest proto.InternalMessageInfo

func (m *DiskDeleteRequest) GetDiskType() DiskType {
	if m != nil {
		return m.DiskType
	}
	return DiskType_filesystem
}

func (m *DiskDeleteRequest) GetDiskName() string {
	if m != nil {
		return m.DiskName
	}
	return ""
}

func (m *DiskDeleteRequest) GetDiskUuid() string {
	if m != nil {
		return m.DiskUuid
	}
	return ""
}

type DiskReplicaInstanceListRequest struct {
	DiskType             DiskType `protobuf:"varint,1,opt,name=disk_type,json=diskType,proto3,enum=imrpc.DiskType" json:"disk_type,omitempty"`
	DiskName             string   `protobuf:"bytes,2,opt,name=disk_name,json=diskName,proto3" json:"disk_name,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *DiskReplicaInstanceListRequest) Reset()         { *m = DiskReplicaInstanceListRequest{} }
func (m *DiskReplicaInstanceListRequest) String() string { return proto.CompactTextString(m) }
func (*DiskReplicaInstanceListRequest) ProtoMessage()    {}
func (*DiskReplicaInstanceListRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{5}
}

func (m *DiskReplicaInstanceListRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskReplicaInstanceListRequest.Unmarshal(m, b)
}
func (m *DiskReplicaInstanceListRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskReplicaInstanceListRequest.Marshal(b, m, deterministic)
}
func (m *DiskReplicaInstanceListRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskReplicaInstanceListRequest.Merge(m, src)
}
func (m *DiskReplicaInstanceListRequest) XXX_Size() int {
	return xxx_messageInfo_DiskReplicaInstanceListRequest.Size(m)
}
func (m *DiskReplicaInstanceListRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskReplicaInstanceListRequest.DiscardUnknown(m)
}

var xxx_messageInfo_DiskReplicaInstanceListRequest proto.InternalMessageInfo

func (m *DiskReplicaInstanceListRequest) GetDiskType() DiskType {
	if m != nil {
		return m.DiskType
	}
	return DiskType_filesystem
}

func (m *DiskReplicaInstanceListRequest) GetDiskName() string {
	if m != nil {
		return m.DiskName
	}
	return ""
}

type DiskReplicaInstanceListResponse struct {
	ReplicaInstances     map[string]*ReplicaInstance `protobuf:"bytes,1,rep,name=replica_instances,json=replicaInstances,proto3" json:"replica_instances,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
	XXX_NoUnkeyedLiteral struct{}                    `json:"-"`
	XXX_unrecognized     []byte                      `json:"-"`
	XXX_sizecache        int32                       `json:"-"`
}

func (m *DiskReplicaInstanceListResponse) Reset()         { *m = DiskReplicaInstanceListResponse{} }
func (m *DiskReplicaInstanceListResponse) String() string { return proto.CompactTextString(m) }
func (*DiskReplicaInstanceListResponse) ProtoMessage()    {}
func (*DiskReplicaInstanceListResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{6}
}

func (m *DiskReplicaInstanceListResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskReplicaInstanceListResponse.Unmarshal(m, b)
}
func (m *DiskReplicaInstanceListResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskReplicaInstanceListResponse.Marshal(b, m, deterministic)
}
func (m *DiskReplicaInstanceListResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskReplicaInstanceListResponse.Merge(m, src)
}
func (m *DiskReplicaInstanceListResponse) XXX_Size() int {
	return xxx_messageInfo_DiskReplicaInstanceListResponse.Size(m)
}
func (m *DiskReplicaInstanceListResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskReplicaInstanceListResponse.DiscardUnknown(m)
}

var xxx_messageInfo_DiskReplicaInstanceListResponse proto.InternalMessageInfo

func (m *DiskReplicaInstanceListResponse) GetReplicaInstances() map[string]*ReplicaInstance {
	if m != nil {
		return m.ReplicaInstances
	}
	return nil
}

type DiskReplicaInstanceDeleteRequest struct {
	DiskType             DiskType `protobuf:"varint,1,opt,name=disk_type,json=diskType,proto3,enum=imrpc.DiskType" json:"disk_type,omitempty"`
	DiskName             string   `protobuf:"bytes,2,opt,name=disk_name,json=diskName,proto3" json:"disk_name,omitempty"`
	DiskUuid             string   `protobuf:"bytes,3,opt,name=disk_uuid,json=diskUuid,proto3" json:"disk_uuid,omitempty"`
	ReplciaInstanceName  string   `protobuf:"bytes,4,opt,name=replcia_instance_name,json=replciaInstanceName,proto3" json:"replcia_instance_name,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *DiskReplicaInstanceDeleteRequest) Reset()         { *m = DiskReplicaInstanceDeleteRequest{} }
func (m *DiskReplicaInstanceDeleteRequest) String() string { return proto.CompactTextString(m) }
func (*DiskReplicaInstanceDeleteRequest) ProtoMessage()    {}
func (*DiskReplicaInstanceDeleteRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{7}
}

func (m *DiskReplicaInstanceDeleteRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskReplicaInstanceDeleteRequest.Unmarshal(m, b)
}
func (m *DiskReplicaInstanceDeleteRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskReplicaInstanceDeleteRequest.Marshal(b, m, deterministic)
}
func (m *DiskReplicaInstanceDeleteRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskReplicaInstanceDeleteRequest.Merge(m, src)
}
func (m *DiskReplicaInstanceDeleteRequest) XXX_Size() int {
	return xxx_messageInfo_DiskReplicaInstanceDeleteRequest.Size(m)
}
func (m *DiskReplicaInstanceDeleteRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskReplicaInstanceDeleteRequest.DiscardUnknown(m)
}

var xxx_messageInfo_DiskReplicaInstanceDeleteRequest proto.InternalMessageInfo

func (m *DiskReplicaInstanceDeleteRequest) GetDiskType() DiskType {
	if m != nil {
		return m.DiskType
	}
	return DiskType_filesystem
}

func (m *DiskReplicaInstanceDeleteRequest) GetDiskName() string {
	if m != nil {
		return m.DiskName
	}
	return ""
}

func (m *DiskReplicaInstanceDeleteRequest) GetDiskUuid() string {
	if m != nil {
		return m.DiskUuid
	}
	return ""
}

func (m *DiskReplicaInstanceDeleteRequest) GetReplciaInstanceName() string {
	if m != nil {
		return m.ReplciaInstanceName
	}
	return ""
}

type DiskVersionResponse struct {
	Version                                 string   `protobuf:"bytes,1,opt,name=version,proto3" json:"version,omitempty"`
	GitCommit                               string   `protobuf:"bytes,2,opt,name=gitCommit,proto3" json:"gitCommit,omitempty"`
	BuildDate                               string   `protobuf:"bytes,3,opt,name=buildDate,proto3" json:"buildDate,omitempty"`
	InstanceManagerDiskServiceAPIVersion    int64    `protobuf:"varint,4,opt,name=instanceManagerDiskServiceAPIVersion,proto3" json:"instanceManagerDiskServiceAPIVersion,omitempty"`
	InstanceManagerDiskServiceAPIMinVersion int64    `protobuf:"varint,5,opt,name=instanceManagerDiskServiceAPIMinVersion,proto3" json:"instanceManagerDiskServiceAPIMinVersion,omitempty"`
	XXX_NoUnkeyedLiteral                    struct{} `json:"-"`
	XXX_unrecognized                        []byte   `json:"-"`
	XXX_sizecache                           int32    `json:"-"`
}

func (m *DiskVersionResponse) Reset()         { *m = DiskVersionResponse{} }
func (m *DiskVersionResponse) String() string { return proto.CompactTextString(m) }
func (*DiskVersionResponse) ProtoMessage()    {}
func (*DiskVersionResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_a4e7e3a42cafcff1, []int{8}
}

func (m *DiskVersionResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_DiskVersionResponse.Unmarshal(m, b)
}
func (m *DiskVersionResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_DiskVersionResponse.Marshal(b, m, deterministic)
}
func (m *DiskVersionResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_DiskVersionResponse.Merge(m, src)
}
func (m *DiskVersionResponse) XXX_Size() int {
	return xxx_messageInfo_DiskVersionResponse.Size(m)
}
func (m *DiskVersionResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_DiskVersionResponse.DiscardUnknown(m)
}

var xxx_messageInfo_DiskVersionResponse proto.InternalMessageInfo

func (m *DiskVersionResponse) GetVersion() string {
	if m != nil {
		return m.Version
	}
	return ""
}

func (m *DiskVersionResponse) GetGitCommit() string {
	if m != nil {
		return m.GitCommit
	}
	return ""
}

func (m *DiskVersionResponse) GetBuildDate() string {
	if m != nil {
		return m.BuildDate
	}
	return ""
}

func (m *DiskVersionResponse) GetInstanceManagerDiskServiceAPIVersion() int64 {
	if m != nil {
		return m.InstanceManagerDiskServiceAPIVersion
	}
	return 0
}

func (m *DiskVersionResponse) GetInstanceManagerDiskServiceAPIMinVersion() int64 {
	if m != nil {
		return m.InstanceManagerDiskServiceAPIMinVersion
	}
	return 0
}

func init() {
	proto.RegisterEnum("imrpc.DiskType", DiskType_name, DiskType_value)
	proto.RegisterType((*Disk)(nil), "imrpc.Disk")
	proto.RegisterType((*ReplicaInstance)(nil), "imrpc.ReplicaInstance")
	proto.RegisterType((*DiskCreateRequest)(nil), "imrpc.DiskCreateRequest")
	proto.RegisterType((*DiskGetRequest)(nil), "imrpc.DiskGetRequest")
	proto.RegisterType((*DiskDeleteRequest)(nil), "imrpc.DiskDeleteRequest")
	proto.RegisterType((*DiskReplicaInstanceListRequest)(nil), "imrpc.DiskReplicaInstanceListRequest")
	proto.RegisterType((*DiskReplicaInstanceListResponse)(nil), "imrpc.DiskReplicaInstanceListResponse")
	proto.RegisterMapType((map[string]*ReplicaInstance)(nil), "imrpc.DiskReplicaInstanceListResponse.ReplicaInstancesEntry")
	proto.RegisterType((*DiskReplicaInstanceDeleteRequest)(nil), "imrpc.DiskReplicaInstanceDeleteRequest")
	proto.RegisterType((*DiskVersionResponse)(nil), "imrpc.DiskVersionResponse")
}

func init() {
	proto.RegisterFile("github.com/longhorn/longhorn-instance-manager/pkg/imrpc/disk.proto", fileDescriptor_a4e7e3a42cafcff1)
}

var fileDescriptor_a4e7e3a42cafcff1 = []byte{
	// 789 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xc4, 0x55, 0xdb, 0x6e, 0xd3, 0x40,
	0x10, 0xc5, 0xb9, 0xb4, 0xc9, 0x04, 0xa5, 0xe9, 0x56, 0x2d, 0x26, 0xa5, 0x34, 0x58, 0x94, 0x56,
	0xa8, 0x75, 0xa4, 0xf4, 0x05, 0x21, 0x84, 0xa0, 0x17, 0xa1, 0x4a, 0x14, 0x55, 0x2e, 0x20, 0x24,
	0x90, 0x2a, 0xc7, 0xd9, 0x3a, 0xab, 0xf8, 0x86, 0x77, 0x5d, 0x91, 0xf2, 0x1b, 0xbc, 0xf0, 0x19,
	0x3c, 0xf1, 0x80, 0xc4, 0xff, 0xf0, 0x17, 0x68, 0x2f, 0xae, 0x9d, 0xd0, 0x94, 0xbc, 0x14, 0xde,
	0xd6, 0x67, 0xce, 0xcc, 0x9c, 0x9d, 0x99, 0x1d, 0xc3, 0x8e, 0x4b, 0x58, 0x3f, 0xe9, 0x9a, 0x4e,
	0xe8, 0xb7, 0xbd, 0x30, 0x70, 0xfb, 0x61, 0x1c, 0x5c, 0x1c, 0xb6, 0x48, 0x40, 0x99, 0x1d, 0x38,
	0x78, 0xcb, 0xb7, 0x03, 0xdb, 0xc5, 0x71, 0x3b, 0x1a, 0xb8, 0x6d, 0xe2, 0xc7, 0x91, 0xd3, 0xee,
	0x11, 0x3a, 0x30, 0xa3, 0x38, 0x64, 0x21, 0x2a, 0x0b, 0xa4, 0xb9, 0xec, 0x86, 0xa1, 0xeb, 0xe1,
	0xb6, 0x00, 0xbb, 0xc9, 0x69, 0x1b, 0xfb, 0x11, 0x1b, 0x4a, 0x8e, 0xf1, 0xa5, 0x00, 0xa5, 0x3d,
	0x42, 0x07, 0xa8, 0x0e, 0x05, 0xd2, 0xd3, 0xb5, 0x96, 0xb6, 0x51, 0xb5, 0x0a, 0xa4, 0x87, 0x10,
	0x94, 0x92, 0x84, 0xf4, 0xf4, 0x82, 0x40, 0xc4, 0x99, 0x63, 0x91, 0xcd, 0xfa, 0x7a, 0x51, 0x62,
	0xfc, 0xcc, 0x31, 0x36, 0x8c, 0xb0, 0x5e, 0x92, 0x18, 0x3f, 0xa3, 0x15, 0x00, 0x16, 0x32, 0xdb,
	0x3b, 0xa1, 0xe4, 0x1c, 0xeb, 0xe5, 0x96, 0xb6, 0x51, 0xb4, 0xaa, 0x02, 0x39, 0x26, 0xe7, 0x18,
	0x2d, 0x43, 0xf5, 0x34, 0xc6, 0x58, 0x5a, 0x67, 0x84, 0xb5, 0xc2, 0x01, 0x61, 0xbc, 0x07, 0x37,
	0xa5, 0x6f, 0xd7, 0x0b, 0x9d, 0x01, 0xd5, 0x67, 0x85, 0xbd, 0x26, 0xb0, 0x1d, 0x01, 0xa1, 0x55,
	0xa8, 0x09, 0x7f, 0xc5, 0xa8, 0x08, 0x06, 0x70, 0x48, 0x11, 0x56, 0x00, 0x84, 0x4d, 0x66, 0xa8,
	0xca, 0xfc, 0x02, 0x49, 0x53, 0x38, 0x5e, 0x42, 0x19, 0x8e, 0x25, 0x01, 0x64, 0x0a, 0x85, 0x71,
	0x8a, 0xf1, 0x4d, 0x83, 0x39, 0x0b, 0x47, 0x1e, 0x71, 0xec, 0x03, 0x55, 0x6b, 0x7e, 0xd3, 0xc0,
	0xf6, 0xb1, 0xaa, 0x91, 0x38, 0x5f, 0x5a, 0xa5, 0x65, 0xa8, 0xf2, 0x26, 0x9c, 0x08, 0xb2, 0x2c,
	0x55, 0x85, 0x03, 0xaf, 0xb8, 0x43, 0x6a, 0x14, 0x5e, 0xa5, 0xcc, 0xf8, 0x46, 0x79, 0xd2, 0x08,
	0x3b, 0x59, 0xd9, 0x4a, 0x56, 0x85, 0x03, 0x42, 0xf5, 0x2a, 0xd4, 0x6c, 0x87, 0x25, 0x69, 0x55,
	0x67, 0x84, 0x19, 0x24, 0x24, 0x34, 0x7f, 0xd7, 0x60, 0x9e, 0xb7, 0x72, 0x37, 0xc6, 0x36, 0xc3,
	0x16, 0xfe, 0x98, 0x60, 0xca, 0xd0, 0xa6, 0x4a, 0x28, 0x9a, 0xc4, 0xa5, 0xd7, 0x3b, 0x73, 0xa6,
	0x18, 0x0c, 0x93, 0x93, 0x5f, 0x0f, 0x23, 0x2c, 0x15, 0xf0, 0xd3, 0xa8, 0xf6, 0xc2, 0x55, 0xda,
	0x8b, 0x7f, 0x6a, 0x17, 0x46, 0x31, 0x20, 0xb9, 0x8b, 0x1d, 0xf1, 0x21, 0x19, 0x6d, 0x48, 0x79,
	0xac, 0x21, 0xc6, 0x27, 0xa8, 0x73, 0x2d, 0x2f, 0x30, 0xbb, 0x46, 0xd5, 0xb9, 0xc9, 0xbd, 0x10,
	0x66, 0x7c, 0x96, 0x25, 0xdb, 0xc3, 0x1e, 0xfe, 0xe7, 0x25, 0x33, 0x06, 0x70, 0x97, 0xc7, 0x1b,
	0x9b, 0xb3, 0x97, 0x84, 0x5e, 0x43, 0x19, 0x8c, 0x5f, 0x1a, 0xac, 0x4e, 0xcc, 0x46, 0xa3, 0x30,
	0xa0, 0x18, 0x11, 0x98, 0x8f, 0xa5, 0xf9, 0x24, 0xdd, 0x30, 0x54, 0xd7, 0x5a, 0xc5, 0x8d, 0x5a,
	0xe7, 0x49, 0x2e, 0xed, 0x15, 0x21, 0xcc, 0x31, 0x1b, 0xdd, 0x0f, 0x58, 0x3c, 0xb4, 0x1a, 0xf1,
	0x18, 0xdc, 0x7c, 0x0f, 0x8b, 0x97, 0x52, 0x51, 0x03, 0x8a, 0x03, 0x3c, 0x54, 0x8f, 0x8c, 0x1f,
	0xd1, 0x26, 0x94, 0xcf, 0x6c, 0x2f, 0x91, 0x57, 0xaa, 0x75, 0x96, 0x94, 0x92, 0x31, 0x77, 0x4b,
	0x92, 0x1e, 0x17, 0x1e, 0x69, 0xc6, 0x4f, 0x0d, 0x5a, 0x97, 0x08, 0xfd, 0x2f, 0x5d, 0x46, 0x1d,
	0x58, 0xe4, 0xb7, 0x77, 0x48, 0x56, 0x54, 0x19, 0x45, 0x3e, 0x92, 0x05, 0x65, 0x4c, 0x45, 0x8a,
	0x66, 0x7d, 0x2d, 0xc0, 0x02, 0x17, 0xf1, 0x16, 0xc7, 0x94, 0x84, 0xc1, 0x45, 0x83, 0x74, 0x98,
	0x3d, 0x93, 0x90, 0x2a, 0x50, 0xfa, 0x89, 0xee, 0x40, 0xd5, 0x25, 0x6c, 0x37, 0xf4, 0x7d, 0xc2,
	0x94, 0xbe, 0x0c, 0xe0, 0xd6, 0x6e, 0x42, 0xbc, 0xde, 0x9e, 0xcd, 0xd2, 0x95, 0x94, 0x01, 0xc8,
	0x82, 0xfb, 0xa9, 0xb2, 0x43, 0xf9, 0x3f, 0xe1, 0xb9, 0x8f, 0x71, 0x7c, 0x46, 0x1c, 0xfc, 0xfc,
	0xe8, 0x40, 0xa9, 0x10, 0x82, 0x8b, 0xd6, 0x54, 0x5c, 0xf4, 0x0e, 0xd6, 0xaf, 0xe4, 0x1d, 0x92,
	0x20, 0x0d, 0x2b, 0xd7, 0xc1, 0xb4, 0xf4, 0x87, 0x6b, 0x50, 0x49, 0xfb, 0x83, 0xea, 0x00, 0xa7,
	0xc4, 0xc3, 0x74, 0x48, 0x19, 0xf6, 0x1b, 0x37, 0x50, 0x15, 0xca, 0x62, 0xab, 0x34, 0xb4, 0xce,
	0x8f, 0x22, 0xd4, 0x72, 0x31, 0xd0, 0x36, 0x40, 0xb6, 0x1c, 0x91, 0x9e, 0xeb, 0xf4, 0xc8, 0xbe,
	0x6c, 0xd6, 0x72, 0x16, 0xf4, 0x54, 0x3a, 0xc9, 0xc1, 0x19, 0x71, 0x1a, 0x99, 0xa5, 0xe6, 0x92,
	0x29, 0xff, 0xb1, 0x66, 0xfa, 0x8f, 0x35, 0xf7, 0xf9, 0x3f, 0x16, 0x6d, 0xc1, 0xac, 0x5a, 0x6c,
	0x68, 0x31, 0xe7, 0x9c, 0x2d, 0xba, 0xd1, 0x74, 0x7d, 0xb8, 0x35, 0xe1, 0x7d, 0xa1, 0xb5, 0xbf,
	0xbd, 0x3f, 0x19, 0xee, 0xc1, 0x74, 0xcf, 0x14, 0x7d, 0x80, 0xdb, 0x13, 0x1f, 0x08, 0x5a, 0x9f,
	0x1c, 0x64, 0xba, 0x6b, 0x3f, 0x03, 0x50, 0xdd, 0xe2, 0x37, 0x9f, 0xc0, 0x6a, 0x36, 0x73, 0x69,
	0xc6, 0x06, 0xbd, 0x3b, 0x23, 0xb8, 0xdb, 0xbf, 0x03, 0x00, 0x00, 0xff, 0xff, 0x56, 0x95, 0x88,
	0x50, 0x07, 0x09, 0x00, 0x00,
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConn

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion4

// DiskServiceClient is the client API for DiskService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type DiskServiceClient interface {
	DiskCreate(ctx context.Context, in *DiskCreateRequest, opts ...grpc.CallOption) (*Disk, error)
	DiskDelete(ctx context.Context, in *DiskDeleteRequest, opts ...grpc.CallOption) (*empty.Empty, error)
	DiskGet(ctx context.Context, in *DiskGetRequest, opts ...grpc.CallOption) (*Disk, error)
	DiskReplicaInstanceList(ctx context.Context, in *DiskReplicaInstanceListRequest, opts ...grpc.CallOption) (*DiskReplicaInstanceListResponse, error)
	DiskReplicaInstanceDelete(ctx context.Context, in *DiskReplicaInstanceDeleteRequest, opts ...grpc.CallOption) (*empty.Empty, error)
	VersionGet(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*DiskVersionResponse, error)
}

type diskServiceClient struct {
	cc *grpc.ClientConn
}

func NewDiskServiceClient(cc *grpc.ClientConn) DiskServiceClient {
	return &diskServiceClient{cc}
}

func (c *diskServiceClient) DiskCreate(ctx context.Context, in *DiskCreateRequest, opts ...grpc.CallOption) (*Disk, error) {
	out := new(Disk)
	err := c.cc.Invoke(ctx, "/imrpc.DiskService/DiskCreate", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *diskServiceClient) DiskDelete(ctx context.Context, in *DiskDeleteRequest, opts ...grpc.CallOption) (*empty.Empty, error) {
	out := new(empty.Empty)
	err := c.cc.Invoke(ctx, "/imrpc.DiskService/DiskDelete", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *diskServiceClient) DiskGet(ctx context.Context, in *DiskGetRequest, opts ...grpc.CallOption) (*Disk, error) {
	out := new(Disk)
	err := c.cc.Invoke(ctx, "/imrpc.DiskService/DiskGet", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *diskServiceClient) DiskReplicaInstanceList(ctx context.Context, in *DiskReplicaInstanceListRequest, opts ...grpc.CallOption) (*DiskReplicaInstanceListResponse, error) {
	out := new(DiskReplicaInstanceListResponse)
	err := c.cc.Invoke(ctx, "/imrpc.DiskService/DiskReplicaInstanceList", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *diskServiceClient) DiskReplicaInstanceDelete(ctx context.Context, in *DiskReplicaInstanceDeleteRequest, opts ...grpc.CallOption) (*empty.Empty, error) {
	out := new(empty.Empty)
	err := c.cc.Invoke(ctx, "/imrpc.DiskService/DiskReplicaInstanceDelete", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *diskServiceClient) VersionGet(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*DiskVersionResponse, error) {
	out := new(DiskVersionResponse)
	err := c.cc.Invoke(ctx, "/imrpc.DiskService/VersionGet", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DiskServiceServer is the server API for DiskService service.
type DiskServiceServer interface {
	DiskCreate(context.Context, *DiskCreateRequest) (*Disk, error)
	DiskDelete(context.Context, *DiskDeleteRequest) (*empty.Empty, error)
	DiskGet(context.Context, *DiskGetRequest) (*Disk, error)
	DiskReplicaInstanceList(context.Context, *DiskReplicaInstanceListRequest) (*DiskReplicaInstanceListResponse, error)
	DiskReplicaInstanceDelete(context.Context, *DiskReplicaInstanceDeleteRequest) (*empty.Empty, error)
	VersionGet(context.Context, *empty.Empty) (*DiskVersionResponse, error)
}

// UnimplementedDiskServiceServer can be embedded to have forward compatible implementations.
type UnimplementedDiskServiceServer struct {
}

func (*UnimplementedDiskServiceServer) DiskCreate(ctx context.Context, req *DiskCreateRequest) (*Disk, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DiskCreate not implemented")
}
func (*UnimplementedDiskServiceServer) DiskDelete(ctx context.Context, req *DiskDeleteRequest) (*empty.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DiskDelete not implemented")
}
func (*UnimplementedDiskServiceServer) DiskGet(ctx context.Context, req *DiskGetRequest) (*Disk, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DiskGet not implemented")
}
func (*UnimplementedDiskServiceServer) DiskReplicaInstanceList(ctx context.Context, req *DiskReplicaInstanceListRequest) (*DiskReplicaInstanceListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DiskReplicaInstanceList not implemented")
}
func (*UnimplementedDiskServiceServer) DiskReplicaInstanceDelete(ctx context.Context, req *DiskReplicaInstanceDeleteRequest) (*empty.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DiskReplicaInstanceDelete not implemented")
}
func (*UnimplementedDiskServiceServer) VersionGet(ctx context.Context, req *empty.Empty) (*DiskVersionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method VersionGet not implemented")
}

func RegisterDiskServiceServer(s *grpc.Server, srv DiskServiceServer) {
	s.RegisterService(&_DiskService_serviceDesc, srv)
}

func _DiskService_DiskCreate_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DiskCreateRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DiskServiceServer).DiskCreate(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/imrpc.DiskService/DiskCreate",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DiskServiceServer).DiskCreate(ctx, req.(*DiskCreateRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _DiskService_DiskDelete_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DiskDeleteRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DiskServiceServer).DiskDelete(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/imrpc.DiskService/DiskDelete",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DiskServiceServer).DiskDelete(ctx, req.(*DiskDeleteRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _DiskService_DiskGet_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DiskGetRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DiskServiceServer).DiskGet(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/imrpc.DiskService/DiskGet",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DiskServiceServer).DiskGet(ctx, req.(*DiskGetRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _DiskService_DiskReplicaInstanceList_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DiskReplicaInstanceListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DiskServiceServer).DiskReplicaInstanceList(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/imrpc.DiskService/DiskReplicaInstanceList",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DiskServiceServer).DiskReplicaInstanceList(ctx, req.(*DiskReplicaInstanceListRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _DiskService_DiskReplicaInstanceDelete_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DiskReplicaInstanceDeleteRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DiskServiceServer).DiskReplicaInstanceDelete(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/imrpc.DiskService/DiskReplicaInstanceDelete",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DiskServiceServer).DiskReplicaInstanceDelete(ctx, req.(*DiskReplicaInstanceDeleteRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _DiskService_VersionGet_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(empty.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(DiskServiceServer).VersionGet(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/imrpc.DiskService/VersionGet",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(DiskServiceServer).VersionGet(ctx, req.(*empty.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

var _DiskService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "imrpc.DiskService",
	HandlerType: (*DiskServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "DiskCreate",
			Handler:    _DiskService_DiskCreate_Handler,
		},
		{
			MethodName: "DiskDelete",
			Handler:    _DiskService_DiskDelete_Handler,
		},
		{
			MethodName: "DiskGet",
			Handler:    _DiskService_DiskGet_Handler,
		},
		{
			MethodName: "DiskReplicaInstanceList",
			Handler:    _DiskService_DiskReplicaInstanceList_Handler,
		},
		{
			MethodName: "DiskReplicaInstanceDelete",
			Handler:    _DiskService_DiskReplicaInstanceDelete_Handler,
		},
		{
			MethodName: "VersionGet",
			Handler:    _DiskService_VersionGet_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "github.com/longhorn/longhorn-instance-manager/pkg/imrpc/disk.proto",
}