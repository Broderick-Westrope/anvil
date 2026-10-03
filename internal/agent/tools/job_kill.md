Terminate a background shell process.

<usage>
- Provide the shell ID returned from a background bash execution or job_list
- Cancels the running process and cleans up resources
</usage>

<outcomes>
- Terminated: the job was running and exited after the kill signal
- Already exited: the job had finished before the kill; the response
  reports its exit code, runtime, and last lines of output, and the job is
  removed from tracking
- Abandoned: the job did not exit within 5s of the kill signal; it is no
  longer tracked and may still hold resources such as ports or files
</outcomes>

<tips>
- Use this when you need to stop a background process
- The process is terminated immediately (similar to SIGTERM)
- After killing, the shell ID becomes invalid
- If a job was abandoned, check for leftover processes or ports before
  starting a replacement
</tips>
