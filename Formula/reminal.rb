class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.4.3"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.3/reminal_0.4.3_darwin_arm64.tar.gz"
      sha256 "2c8099f464817b17074565ed076303ad99a1f56f50b173fcd1dddd68f21fab1a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.3/reminal_0.4.3_darwin_amd64.tar.gz"
      sha256 "4c586567092a7b05273b8d166169f68ee01f53fa5139bbb82caeff8174668d85"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.3/reminal_0.4.3_linux_arm64.tar.gz"
      sha256 "3c4f121aa8319f4c040f0838bd53c25354164d38f3feae63875307838e52fc27"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
