"""Install only the dashboard bundle. Never modify host service executables."""
import pathlib
import plistlib
import shutil
import subprocess
import sys
import tempfile
import time


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout.strip()


def install(parent):
    parent = pathlib.Path(parent).expanduser().resolve()
    parent.mkdir(parents=True, exist_ok=True)
    destination = parent / "ClawFlow.app"
    source = pathlib.Path(__file__).resolve().parent / "dist/ClawFlow.app"
    with tempfile.TemporaryDirectory(prefix=".clawflow-dev-", dir=parent) as temporary:
        stage = pathlib.Path(temporary) / "ClawFlow.app"
        run("/usr/bin/ditto", str(source), str(stage))
        run("/usr/bin/codesign", "--verify", "--deep", "--strict", str(stage))
        with (stage / "Contents/Info.plist").open("rb") as file:
            info = plistlib.load(file)
        version = info["CFBundleShortVersionString"]
        if run(str(stage / "Contents/Helpers/clawflow"), "version").splitlines()[0].removeprefix("v") != version:
            raise RuntimeError("App and bundled CLI versions differ")
        if info.get("ClawFlowBuildChannel") != "dev":
            raise RuntimeError("Only dev bundles may use this installer")
        # Ask only this app to quit; services have independent launchd lifecycles.
        run("/usr/bin/osascript", "-e", 'tell application id "com.clawflow.dashboard" to quit')
        time.sleep(1)
        previous = pathlib.Path(temporary) / "previous.app"
        if destination.exists():
            destination.rename(previous)
        try:
            stage.rename(destination)
            run("/usr/bin/open", "-g", str(destination))
        except Exception:
            if destination.exists():
                shutil.rmtree(destination)
            if previous.exists():
                previous.rename(destination)
                run("/usr/bin/open", "-g", str(destination))
            raise
    print(f"Installed and relaunched ClawFlow {version}: {destination}")


if __name__ == "__main__":
    install(sys.argv[1])
