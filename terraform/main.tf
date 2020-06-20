module "network" {
  source                = "./network"
  vpc_cidr_block        = var.vpc_cidr_block
  public_subnets_cidrs  = var.public_subnets_cidrs
  private_subnets_cidrs = var.private_subnets_cidrs
  name_prefix           = var.name_prefix
  lb_internal           = var.lb_internal
  common_tags           = var.common_tags
}

module "external_services" {
  source                     = "./ext-services"
  s3_bucket_name             = var.s3_bucket_name
  s3_bucket_region           = var.s3_bucket_region
  pg_vpc_id                  = module.network.vpc_id
  pg_subnet_ids              = module.network.private_subnets_ids
  pg_identifier_prefix       = var.name_prefix
  pg_instance_class          = var.pg_instance_class
  pg_engine_version          = var.pg_engine_version
  pg_allocated_storage       = var.pg_allocated_storage
  pg_storage_type            = var.pg_storage_type
  pg_multi_az                = var.pg_multi_az
  pg_parameter_group_name    = var.pg_parameter_group_name
  pg_deletion_protection     = var.pg_deletion_protection
  pg_backup_retention_period = var.pg_backup_retention_period
  pg_db_name                 = var.pg_db_name
  pg_username                = var.pg_username
  pg_password                = var.pg_password
  common_tags                = var.common_tags
}

module "key_pair" {
  source          = "./key-pair"
  key_name        = var.key_name
  key_pair_create = var.key_pair_create
  public_key      = var.public_key_path == "" ? "" : file(var.public_key_path)
}

module "tfe_instance" {
  source                      = "./asg-ec2-instance"
  vpc_id                      = module.network.vpc_id
  subnets_ids                 = var.tfe_associate_public_ip_address ? module.network.public_subnets_ids : module.network.private_subnets_ids
  ami_id                      = var.ami_id
  key_name                    = var.key_pair_create ? module.key_pair.key_pair_name : var.key_name
  instance_type               = var.instance_type
  name_prefix                 = var.name_prefix
  target_groups_arns          = [module.network.lb_tg_80_arn, module.network.lb_tg_443_arn, module.network.lb_tg_8800_arn]
  associate_public_ip_address = var.tfe_associate_public_ip_address
  root_block_device_size      = var.root_block_device_size
  health_check_type           = var.health_check_type
  replicated_password         = var.replicated_password
  tfe_hostname                = var.tfe_hostname
  tfe_enc_password            = var.tfe_enc_password
  tfe_pg_dbname               = var.pg_db_name
  tfe_pg_address              = module.external_services.pg_address
  tfe_pg_password             = var.pg_password
  tfe_pg_user                 = var.pg_username
  tfe_s3_bucket               = module.external_services.s3_bucket_name
  tfe_s3_region               = var.s3_bucket_region
  common_tags                 = var.common_tags
}

locals {
  tfe_hostname_split = split(".", var.tfe_hostname)
}

module "tfe_dns" {
  source           = "./dns"
  cname_value      = module.network.lb_dns_name
  cname_record     = element(local.tfe_hostname_split, 0)
  hosted_zone_name = join(".", slice(local.tfe_hostname_split, 1, length(local.tfe_hostname_split)))
}

module "ssh_hop" {
  source              = "./ssh-hop"
  enable              = var.create_ssh_hop
  name_prefix         = var.name_prefix
  subnet_id           = module.network.public_subnets_ids[0]
  vpc_id              = module.network.vpc_id
  allow_ingress_cirds = var.ssh_ingress_cidrs
  key_name            = var.key_name
  common_tags         = var.common_tags
}
