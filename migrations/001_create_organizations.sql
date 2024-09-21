-- +goose Up
CREATE TABLE Organization (
    organization_id varchar(32)  NOT NULL,
    name            varchar(100) NOT NULL,
    PRIMARY KEY(organization_id)
);

-- +goose Down
DROP TABLE Organization;
