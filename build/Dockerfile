FROM registry.ci.openshift.org/stolostron/builder:go1.23-linux AS builder

WORKDIR /go/src/github.com/open-cluster-management-io/multicloud-integrations
COPY . .
RUN make -f Makefile build

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

RUN microdnf update -y && \
     microdnf clean all

ENV OPERATOR=/usr/local/bin/multicloud-integrations \
    USER_UID=1001 \
    USER_NAME=multicloud-integrations

# install operator binary
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/gitopscluster /usr/local/bin/gitopscluster
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/gitopssyncresc /usr/local/bin/gitopssyncresc
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/multiclusterstatusaggregation /usr/local/bin/multiclusterstatusaggregation
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/propagation /usr/local/bin/propagation
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/gitopsaddon /usr/local/bin/gitopsaddon
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/maestroPropagation /usr/local/bin/maestroPropagation
COPY --from=builder /go/src/github.com/open-cluster-management-io/multicloud-integrations/build/_output/bin/maestroAggregation /usr/local/bin/maestroAggregation

COPY build/bin /usr/local/bin
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
