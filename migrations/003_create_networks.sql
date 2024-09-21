-- +goose Up
CREATE TABLE Network (
    network_id      varchar(32)  NOT NULL,
    organization_id varchar(32)  NOT NULL,
    name            varchar(100) NOT NULL,
    PRIMARY KEY(network_id),
    FOREIGN KEY (organization_id) REFERENCES organization (organization_id)
);

-- +goose Down
DROP TABLE Network;
