class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.5.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.0/reminal_0.5.0_darwin_arm64.tar.gz"
      sha256 "ea3eba6e5b0da7f786a9e0781ee0e7c1a5e8325d860d01e34c13bd1db649c65d"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.0/reminal_0.5.0_darwin_amd64.tar.gz"
      sha256 "ac38f67d77013b1d4738cbda1a8cf7068c9546bea538b942c1907147cdab198a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.0/reminal_0.5.0_linux_arm64.tar.gz"
      sha256 "4ff34d75ba49b6dc651f79917abfdd1a94e4c704a74d51e3d8dad94f05681e49"
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
