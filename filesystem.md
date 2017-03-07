# Jiri filesystem

All data managed by the jiri tool is located in the file system under a root directory, colloquially called the jiri root directory.  The file system layout looks like this:
```
 [root]                              # root directory (name picked by user)
 [root]/.jiri_root                   # root metadata directory
 [root]/.jiri_root/bin               # contains jiri tool binary
 [root]/.jiri_root/update_history    # contains history of update snapshots
 [root]/.manifest                    # contains jiri manifests
 [root]/[project1]                   # project directory (name picked by user)
 [root]/[project1]/.jiri             # project metadata directory
 [root]/[project1]/.jiri/metadata.v2 # project metadata file
 [root]/[project1]/.jiri/<<cls>>     # project per-cl metadata directories
 [root]/[project1]/<<files>>         # project files
 [root]/[project2]...
```
The [root] and [projectN] directory names are picked by the user.  The <<cls>> are named via jiri cl new, and the <<files>> are named as the user adds files and directories to their project.  All other names
above have special meaning to the jiri tool, and cannot be changed; you must ensure your path names don't collide with these special names.

To find the [root] directory, the jiri binary looks for the .jiri\_root directory, starting in the current working directory and walking up the directory chain.  The search is terminated successfully when the
.jiri\_root directory is found; it fails after it reaches the root of the file system. Thus jiri must be invoked from the [root] directory or one of its subdirectories.  To invoke jiri from a different
directory, you can set the -root flag to point to your [root] directory.

Keep in mind that when "jiri update" is run, the jiri tool itself is automatically updated along with all projects.  Note that if you have multiple [root] directories on your file system, you must remember to
run the jiri binary corresponding to your [root] directory.  Things may fail if you mix things up, since the jiri binary is updated with each call to "jiri update", and you may encounter version mismatches
between the jiri binary and the various metadata files or other logic.

The jiri binary is located at [root]/.jiri\_root/bin/jiri
