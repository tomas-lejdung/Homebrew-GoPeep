class Gopeep < Formula
  desc "P2P screen sharing for pair programming using WebRTC"
  homepage "https://github.com/tomas-lejdung/Homebrew-GoPeep"
  url "https://github.com/tomas-lejdung/Homebrew-GoPeep.git", branch: "main"
  version "1.0.0"
  license "MIT"

  depends_on "go" => :build
  depends_on "pkg-config" => :build
  depends_on "libvpx"
  depends_on :macos

  def install
    # Build with CGO for ScreenCaptureKit and libvpx
    ENV["CGO_ENABLED"] = "1"
    
    system "go", "build", *std_go_args(ldflags: "-s -w"), "-o", bin/"gopeep", "."
    
    # Ad-hoc sign the binary for macOS Gatekeeper
    system "codesign", "--sign", "-", "--force", bin/"gopeep"
  end

  def caveats
    <<~EOS
      GoPeep requires Screen Recording permission.
      
      On first run, macOS will prompt you to grant access in:
        System Preferences > Privacy & Security > Screen Recording
      
      After granting permission, restart gopeep.
    EOS
  end

  test do
    assert_match "GoPeep", shell_output("#{bin}/gopeep --help")
  end
end
