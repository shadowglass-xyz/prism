-- +goose Up
CREATE TABLE Container (
    container_id    varchar(32)  NOT NULL,
    organization_id varchar(32)  NOT NULL,
    name            varchar(100) NOT NULL,
    PRIMARY KEY(container_id),
    FOREIGN KEY (organization_id) REFERENCES organization (organization_id)
);

-- +goose Down
DROP TABLE Container;
