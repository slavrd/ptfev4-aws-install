locals {
  asg_name = "${var.name_prefix}ptfe-asg"
  asg_hook = "${var.name_prefix}ptfe-lifecycle-hook"
}

resource "aws_launch_configuration" "ptfe" {
  name_prefix                 = var.name_prefix
  image_id                    = var.ami_id
  instance_type               = var.instance_type
  iam_instance_profile        = aws_iam_instance_profile.ptfe_instance.name
  key_name                    = var.key_name
  associate_public_ip_address = var.associate_public_ip_address
  security_groups             = [aws_security_group.ptfe_instance.id]

  root_block_device {
    volume_size = var.root_block_device_size
  }

  user_data_base64 = base64encode(templatefile("${path.module}/templates/cloud-init.tmpl", {
    replicated_conf_b64content = base64encode(templatefile("${path.module}/templates/replicated.conf.tmpl", {
      ptfe_hostname       = var.ptfe_hostname
      replicated_password = var.replicated_password
    }))

    ptfe_settings_b64content = base64encode(templatefile("${path.module}/templates/settings.json.tmpl", {
      ptfe_enc_password = var.ptfe_enc_password
      ptfe_hostname     = var.ptfe_hostname
      ptfe_pg_dbname    = var.ptfe_pg_dbname
      ptfe_pg_address   = var.ptfe_pg_address
      ptfe_pg_password  = var.ptfe_pg_password
      ptfe_pg_user      = var.ptfe_pg_user
      ptfe_s3_bucket    = var.ptfe_s3_bucket
      ptfe_s3_region    = var.ptfe_s3_region
    }))

    install_wrapper_b64content = base64encode(templatefile("${path.module}/templates/install_wrap.sh.tmpl", {
      asg_hook = local.asg_hook
      asg_name = local.asg_name
    }))
  }))

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_autoscaling_group" "ptfe" {
  name                      = local.asg_name
  max_size                  = 1
  min_size                  = 1
  health_check_grace_period = 300
  health_check_type         = "ELB"
  launch_configuration      = aws_launch_configuration.ptfe.name
  vpc_zone_identifier       = var.subnets_ids
  target_group_arns         = var.target_groups_arns
  wait_for_capacity_timeout = 0 # installing / starting PTFE can take ~30-40 mins so no point terraform waiting for capacity.
  initial_lifecycle_hook {
    name                 = local.asg_hook
    default_result       = "ABANDON"
    heartbeat_timeout    = 1800
    lifecycle_transition = "autoscaling:EC2_INSTANCE_LAUNCHING"
  }

  dynamic "tag" {
    for_each = merge({ Name = "${var.name_prefix}instance" }, var.common_tags)
    content {
      key                 = tag.key
      value               = tag.value
      propagate_at_launch = true
    }
  }

  timeouts {
    delete = "15m"
  }
}