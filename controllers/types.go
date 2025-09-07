package controllers

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// UnstructuredLabSession wraps unstructured.Unstructured for LabSession
type UnstructuredLabSession struct {
	unstructured.Unstructured
}

// UnstructuredLabSessionList wraps unstructured.UnstructuredList for LabSession
type UnstructuredLabSessionList struct {
	unstructured.UnstructuredList
}

// DeepCopyObject creates a deep copy of UnstructuredLabSession
func (u *UnstructuredLabSession) DeepCopyObject() runtime.Object {
	return &UnstructuredLabSession{
		Unstructured: *u.Unstructured.DeepCopy(),
	}
}

// DeepCopyObject creates a deep copy of UnstructuredLabSessionList
func (u *UnstructuredLabSessionList) DeepCopyObject() runtime.Object {
	return &UnstructuredLabSessionList{
		UnstructuredList: *u.UnstructuredList.DeepCopy(),
	}
}