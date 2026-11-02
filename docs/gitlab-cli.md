# GitLab CI 操作参考

这些命令用于已有 GitLab 项目的 CI 运维，不是 ClawFlow 本机调度的启动方式。返回[项目入口](../readme.md)。

## Trigger a manual GitLab deployment

After approving a change and its target environment, start an existing manual
deployment job from the CLI:

```bash
clawflow ci pipeline list --repo https://gitlab.company.com/group/project --ref main
clawflow ci pipeline view --repo https://gitlab.company.com/group/project --id 12345
# Select the manual deployment job ID from the pipeline's job list.
clawflow ci job play --repo https://gitlab.company.com/group/project --id 67890
clawflow ci job log --repo https://gitlab.company.com/group/project --job 67890
```

`job play` (alias `job trigger`) calls GitLab's
[manual job API](https://docs.gitlab.com/api/jobs/#run-a-job), equivalent to its
play button. Keep `when: manual`; the selected job defines the deployment target.
The command returns the accepted job status and URL; check the pipeline and logs
for completion. Add `--json` for structured output. Your GitLab token needs `api`
scope and permission to run the job; protected-environment approvals still apply.

## Manage project CI/CD variables

Manage the variables on GitLab's **Settings > CI/CD** page:

```bash
clawflow ci variable list --repo https://gitlab.company.com/group/project
clawflow ci variable view --repo https://gitlab.company.com/group/project --key DEPLOY_TARGET
clawflow ci variable create --repo https://gitlab.company.com/group/project --key DEPLOY_TARGET --value staging
clawflow ci variable update --repo https://gitlab.company.com/group/project --key DEPLOY_TARGET --value qa
clawflow ci variable delete --repo https://gitlab.company.com/group/project --key DEPLOY_TARGET
```

Values are redacted by default, including `--json` output. Add `--show-values`
to display values explicitly. For secrets or multiline/file variables, use
`--value-file /path/to/value` or `--value-file -` for stdin instead of putting
the value on the command line. Create and update require a value, which can be
an explicit empty string (`--value ''`). Optional flags include `--type env_var|file`,
`--protected`, `--masked`, `--raw`, and `--description`; omitted settings are
preserved during updates, and explicit boolean false values are supported.

For environment-specific variables, `--scope production` sets the scope on
create and selects the exact existing scope on view/update/delete. These use
GitLab's [project variables API](https://docs.gitlab.com/api/project_level_variables/);
newer attributes and environment scopes depend on your server's support. The
commands reuse the configured GitLab token and enforce the Pilot write budget.

